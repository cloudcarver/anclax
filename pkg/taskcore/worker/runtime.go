package worker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/pkg/metrics"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/google/uuid"
)

type RuntimeOptions struct {
	PollInterval          time.Duration
	HeartbeatInterval     time.Duration
	RuntimeConfigInterval time.Duration
	OperationTimeout      time.Duration
	ShutdownTimeout       time.Duration
	OnError               func(error)
}

func DefaultRuntimeOptions() RuntimeOptions {
	return RuntimeOptions{PollInterval: time.Second, HeartbeatInterval: 3 * time.Second, OperationTimeout: 5 * time.Second, ShutdownTimeout: 5 * time.Second}
}

type taskRequest struct {
	id     string
	taskID int32
	task   *Task
	ctx    context.Context
	result chan error
}
type runtimeEnvelope struct {
	ctx           context.Context
	event         Event
	events        []Event
	done          chan struct{}
	snapshot      chan Snapshot
	op            func()
	skip          bool
	request       *taskRequest
	operationDone bool
}

// Runtime owns the Engine and all asynchronous operations. Only this event loop
// changes admission state; manual execution uses the same request/result protocol.
type Runtime struct {
	engine   *Engine
	port     Port
	opts     RuntimeOptions
	inbox    chan runtimeEnvelope
	ctx      context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
	stopCh   chan struct{}
	loopDone chan struct{}

	// Owned by eventLoop.
	started        bool
	stopping       bool
	activeOps      int
	offlinePending bool
	requests       map[string]*taskRequest
}

func NewRuntime(engine *Engine, port Port, opts RuntimeOptions) *Runtime {
	opts.PollInterval = max(0, opts.PollInterval)
	opts.HeartbeatInterval = max(0, opts.HeartbeatInterval)
	opts.RuntimeConfigInterval = max(0, opts.RuntimeConfigInterval)
	if opts.OperationTimeout <= 0 {
		opts.OperationTimeout = 5 * time.Second
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{engine: engine, port: port, opts: opts, inbox: make(chan runtimeEnvelope, 2048), ctx: ctx, cancel: cancel,
		stopCh: make(chan struct{}), loopDone: make(chan struct{}), requests: make(map[string]*taskRequest)}
	go r.eventLoop()
	return r
}

func (r *Runtime) requestStop() { r.stopOnce.Do(func() { r.cancel(); close(r.stopCh) }) }
func (r *Runtime) Close()       { r.requestStop(); <-r.loopDone }
func (r *Runtime) NotifyRuntimeConfig(ctx context.Context, requestID string) {
	r.enqueue(ctx, Event{Type: EventRuntimeConfigNotify, RequestID: requestID}, false)
}
func (r *Runtime) Step(ctx context.Context, event Event) { r.enqueue(ctx, event, true) }

func (r *Runtime) send(ctx context.Context, env runtimeEnvelope) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return false
	case <-r.loopDone:
		return false
	case r.inbox <- env:
		return true
	}
}
func (r *Runtime) enqueue(ctx context.Context, event Event, wait bool) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	env := runtimeEnvelope{ctx: ctx, event: event}
	if wait {
		env.done = make(chan struct{})
	}
	if !r.send(ctx, env) {
		return false
	}
	if !wait {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-r.loopDone:
		return false
	case <-env.done:
		return true
	}
}

func (r *Runtime) Snapshot(ctx context.Context) (Snapshot, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	env := runtimeEnvelope{ctx: ctx, skip: true, snapshot: make(chan Snapshot, 1)}
	if !r.send(ctx, env) {
		return Snapshot{}, false
	}
	select {
	case <-ctx.Done():
		return Snapshot{}, false
	case <-r.loopDone:
		return Snapshot{}, false
	case s := <-env.snapshot:
		return s, true
	}
}

func (r *Runtime) RunTask(ctx context.Context, taskID int32) error {
	if taskID <= 0 {
		return fmt.Errorf("taskID must be positive")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if r.ctx.Err() != nil {
		return context.Canceled
	}
	req := &taskRequest{id: uuid.NewString(), taskID: taskID, ctx: ctx, result: make(chan error, 1)}
	if lookup, ok := r.port.(TaskLookupPort); ok {
		lookupCtx, cancel := context.WithTimeout(ctx, r.opts.OperationTimeout)
		stop := context.AfterFunc(r.ctx, cancel)
		task, err := lookup.LookupTask(lookupCtx, taskID)
		stop()
		cancel()
		if errors.Is(err, ErrNoTask) {
			return nil
		}
		if err != nil {
			return err
		}
		req.task = task
	}
	if !r.send(ctx, runtimeEnvelope{ctx: ctx, request: req}) {
		return context.Canceled
	}
	select {
	case err := <-req.result:
		return err
	case <-r.loopDone:
		return context.Canceled
	case <-ctx.Done():
		r.enqueue(r.ctx, Event{Type: EventCancelTaskRequest, RequestID: req.id}, false)
		return ctx.Err()
	}
}

func (r *Runtime) startupCatchUpRuntimeConfig(ctx context.Context) error {
	var cfg *RuntimeConfig
	err := r.call(ctx, func(callCtx context.Context) error {
		var err error
		cfg, err = r.port.RefreshRuntimeConfig(callCtx, r.engine.WorkerID(), "")
		return err
	})
	if err != nil {
		return err
	}
	env := runtimeEnvelope{ctx: ctx, skip: true, done: make(chan struct{}), op: func() {
		if cfg != nil && cfg.Version > r.engine.CurrentRuntimeConfigVersion() {
			r.engine.applyRuntimeConfig(*cfg)
		}
	}}
	if !r.send(ctx, env) {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.loopDone:
		return context.Canceled
	case <-env.done:
		return nil
	}
}

// Startup I/O also belongs to the runtime's operation count. In particular,
// shutdown must wait for registration before writing the final offline marker.
func (r *Runtime) call(ctx context.Context, fn func(context.Context) error) error {
	done := make(chan error, 1)
	env := runtimeEnvelope{ctx: ctx, skip: true, op: func() {
		if r.stopping {
			done <- context.Canceled
			return
		}
		r.spawn(ctx, false, r.opts.OperationTimeout, func(callCtx context.Context) []Event {
			done <- fn(callCtx)
			return nil
		})
	}}
	if !r.send(ctx, env) {
		return context.Canceled
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-r.loopDone:
		return context.Canceled
	}
}

func (r *Runtime) eventLoop() {
	defer close(r.loopDone)
	defer func() {
		for _, req := range r.requests {
			req.result <- context.Canceled
		}
	}()
	stopCh := r.stopCh
	var deadline <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-stopCh:
			stopCh = nil
			r.stopping = true
			timer = time.NewTimer(r.opts.ShutdownTimeout)
			deadline = timer.C
			r.processEvent(context.Background(), Event{Type: EventStop})
		case <-deadline:
			r.handleError(fmt.Errorf("worker shutdown timed out with %d operations still active", r.activeOps))
			return
		case env := <-r.inbox:
			if env.operationDone {
				r.activeOps--
			}
			if env.request != nil {
				req := env.request
				if r.stopping || r.engine.stopped {
					req.result <- context.Canceled
				} else {
					r.requests[req.id] = req
					r.processEvent(req.ctx, Event{Type: EventRunTask, TaskID: req.taskID, Task: req.task, RequestID: req.id})
				}
			} else if env.op != nil {
				env.op()
			} else if !env.skip {
				if len(env.events) > 0 {
					for _, event := range env.events {
						r.processEvent(env.ctx, event)
					}
				} else {
					r.processEvent(env.ctx, env.event)
				}
			}
			if env.snapshot != nil {
				env.snapshot <- r.engine.Snapshot()
			}
			if env.done != nil {
				close(env.done)
			}
		}
		// Mark offline only after preceding heartbeat/execution operations have
		// drained, so a late heartbeat cannot put the worker back online.
		if r.offlinePending && r.activeOps == 0 {
			r.offlinePending = false
			r.spawn(context.Background(), true, r.opts.OperationTimeout, func(ctx context.Context) []Event {
				r.handleError(r.port.MarkWorkerOffline(ctx, r.engine.WorkerID()))
				return nil
			})
		} else if r.stopping && r.activeOps == 0 {
			return
		}
	}
}

func (r *Runtime) processEvent(ctx context.Context, event Event) {
	queue := []Event{event}
	for len(queue) > 0 {
		ev := queue[0]
		queue = queue[1:]
		for _, cmd := range r.engine.Apply(ev) {
			queue = append(queue, r.execCommand(ctx, cmd)...)
		}
		if r.started && !r.stopping && (ev.Type == EventPollTick || ev.Type == EventFinalizeResult) {
			// Fill all available business slots, then sleep on the poll ticker
			// after an empty claim. Empty results never trigger a refill loop.
			for {
				commands := r.engine.Apply(Event{Type: EventPollTick})
				if len(commands) == 0 {
					break
				}
				for _, cmd := range commands {
					queue = append(queue, r.execCommand(r.ctx, cmd)...)
				}
			}
			for _, cmd := range r.engine.Apply(Event{Type: EventControlPollTick}) {
				queue = append(queue, r.execCommand(r.ctx, cmd)...)
			}
		}
	}
	s := r.engine.Snapshot()
	metrics.WorkerTaskPhases.WithLabelValues("claiming").Set(float64(s.Claiming))
	metrics.WorkerTaskPhases.WithLabelValues("executing").Set(float64(s.Executing))
	metrics.WorkerTaskPhases.WithLabelValues("finalizing").Set(float64(s.Finalizing))
	metrics.WorkerGoroutines.Set(float64(s.InFlight + s.ControlInFlight))
	metrics.WorkerStrictInFlight.Set(float64(s.StrictInFlight))
	metrics.WorkerStrictCap.Set(float64(s.StrictCap))
	metrics.WorkerRuntimeConfigVersion.Set(float64(s.RuntimeConfigVersion))
}

// spawn is called only by the event loop. Completion always returns to it, even
// after the originating request is canceled. Finalization has its own deadline.
func (r *Runtime) spawn(ctx context.Context, detached bool, timeout time.Duration, fn func(context.Context) []Event) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.activeOps++
	go func() {
		base := ctx
		if detached {
			base = context.WithoutCancel(ctx)
		}
		opCtx, cancel := context.WithCancel(base)
		defer cancel()
		stop := func() bool { return false }
		if !detached {
			stop = context.AfterFunc(r.ctx, cancel)
			if r.ctx.Err() != nil {
				cancel()
			}
		}
		defer stop()
		if timeout > 0 {
			var timeoutCancel context.CancelFunc
			opCtx, timeoutCancel = context.WithTimeout(opCtx, timeout)
			defer timeoutCancel()
		}
		events := fn(opCtx)
		// Request cancellation must not drop a claimed task's finalization.
		r.send(context.Background(), runtimeEnvelope{ctx: ctx, events: events, skip: len(events) == 0, operationDone: true})
	}()
}

func (r *Runtime) Start(ctx context.Context) {
	defer r.Close()
	if err := r.startupCatchUpRuntimeConfig(ctx); err != nil {
		r.handleError(err)
		return
	}
	snapshot, ok := r.Snapshot(ctx)
	if !ok {
		return
	}
	err := r.call(ctx, func(registerCtx context.Context) error {
		return r.port.RegisterWorker(registerCtx, r.engine.WorkerID(), r.engine.Labels(), snapshot.RuntimeConfigVersion)
	})
	if err != nil {
		r.handleError(err)
		return
	}
	if !r.send(ctx, runtimeEnvelope{ctx: ctx, skip: true, op: func() { r.started = true }}) {
		return
	}
	var tickers []*time.Ticker
	ticker := func(interval time.Duration) <-chan time.Time {
		if interval <= 0 {
			return nil
		}
		t := time.NewTicker(interval)
		tickers = append(tickers, t)
		return t.C
	}
	pollCh, heartCh, configCh := ticker(r.opts.PollInterval), ticker(r.opts.HeartbeatInterval), ticker(r.opts.RuntimeConfigInterval)
	defer func() {
		for _, t := range tickers {
			t.Stop()
		}
	}()
	if r.opts.PollInterval > 0 {
		r.enqueue(ctx, Event{Type: EventPollTick}, false)
	}
	for {
		select {
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		case <-pollCh:
			r.enqueue(ctx, Event{Type: EventPollTick}, false)
		case <-heartCh:
			r.enqueue(ctx, Event{Type: EventHeartbeatTick}, false)
		case <-configCh:
			r.enqueue(ctx, Event{Type: EventRuntimeConfigTick}, false)
		}
	}
}

func (r *Runtime) execCommand(ctx context.Context, cmd Command) []Event {
	if req := r.requests[cmd.RequestID]; req != nil {
		ctx = req.ctx
	}
	switch cmd.Type {
	case CmdTaskRequestDone:
		if req := r.requests[cmd.RequestID]; req != nil {
			req.result <- cmd.Err
			delete(r.requests, cmd.RequestID)
		}
	case CmdClaimBatch:
		r.spawn(ctx, false, r.opts.OperationTimeout, func(ctx context.Context) []Event {
			port, ok := r.port.(BatchPort)
			if !ok {
				err := errors.New("batch admission requires a BatchPort")
				r.handleError(err)
				return []Event{{Type: EventClaimBatchResult, CycleID: cmd.CycleID, Err: err}}
			}
			tasks, err := port.ClaimBatch(ctx, ClaimBatchRequest{BatchSize: cmd.BatchSize,
				StrictSlots: cmd.StrictSlots, Groups: cmd.Groups, WeightedLabels: cmd.WeightedLabels})
			r.handleError(err)
			metrics.PulledTasks.Add(float64(len(tasks)))
			return []Event{{Type: EventClaimBatchResult, CycleID: cmd.CycleID, Tasks: tasks, Err: err}}
		})
	case CmdClaimStrict, CmdClaimNormal, CmdClaimControl, CmdClaimByID:
		req := ClaimRequest{WorkerID: r.engine.WorkerID(), Labels: r.engine.Labels(), AllowStrict: cmd.AllowStrict}
		req.HasLabels = len(req.Labels) > 0
		r.spawn(ctx, false, r.opts.OperationTimeout, func(ctx context.Context) []Event {
			var task *Task
			var err error
			var eventType EventType
			switch cmd.Type {
			case CmdClaimStrict:
				eventType = EventClaimStrictResult
				task, err = r.port.ClaimStrict(ctx, req)
			case CmdClaimNormal:
				eventType = EventClaimNormalResult
				task, err = r.port.ClaimNormalByGroup(ctx, ClaimNormalRequest{ClaimRequest: req, Group: cmd.Group, WeightedLabels: cmd.WeightedLabels})
			case CmdClaimControl:
				eventType = EventClaimControlResult
				if port, ok := r.port.(ControlPort); ok {
					task, err = port.ClaimControl(ctx, req)
				} else {
					err = ErrNoTask
				}
			case CmdClaimByID:
				eventType = EventClaimByIDResult
				task, err = r.port.ClaimByID(ctx, cmd.TaskID, req)
			}
			if errors.Is(err, ErrNoTask) {
				err = nil
				task = nil
			} else if err != nil {
				r.handleError(err)
			}
			if task != nil {
				metrics.PulledTasks.Inc()
			}
			return []Event{{Type: eventType, CycleID: cmd.CycleID, Task: copyTask(task), Err: err}}
		})
	case CmdExecuteTask:
		if cmd.Task == nil {
			return []Event{{Type: EventExecuteResult, CycleID: cmd.CycleID}}
		}
		task := *cmd.Task
		r.spawn(ctx, false, 0, func(ctx context.Context) []Event {
			err := r.executeSafely(ctx, task)
			if errors.Is(err, context.Canceled) {
				err = taskcore.ErrTaskInterrupted
			}
			return []Event{{Type: EventExecuteResult, CycleID: cmd.CycleID, ExecErr: err}}
		})
	case CmdFinalize:
		if cmd.Task == nil {
			return []Event{{Type: EventFinalizeResult, CycleID: cmd.CycleID}}
		}
		task := *cmd.Task
		r.spawn(ctx, true, r.opts.OperationTimeout, func(ctx context.Context) []Event {
			err := r.port.FinalizeTask(ctx, task, cmd.ExecErr)
			r.handleError(err)
			return []Event{{Type: EventFinalizeResult, CycleID: cmd.CycleID, Err: err}}
		})
	case CmdHeartbeat:
		r.spawn(ctx, false, r.opts.OperationTimeout, func(ctx context.Context) []Event {
			if err := r.port.Heartbeat(ctx, r.engine.WorkerID()); err != nil && r.ctx.Err() == nil {
				r.handleError(err)
				r.requestStop()
			}
			return nil
		})
	case CmdRefreshRuntimeConfig:
		r.spawn(ctx, false, r.opts.OperationTimeout, func(ctx context.Context) []Event {
			cfg, err := r.port.RefreshRuntimeConfig(ctx, r.engine.WorkerID(), cmd.RequestID)
			r.handleError(err)
			return []Event{{Type: EventRuntimeConfigLoaded, RequestID: cmd.RequestID, Config: cfg, Err: err}}
		})
	case CmdAckRuntimeConfig:
		r.spawn(ctx, false, r.opts.OperationTimeout, func(ctx context.Context) []Event {
			r.handleError(r.port.AckRuntimeConfigApplied(ctx, r.engine.WorkerID(), cmd.RequestID, cmd.AppliedVersion))
			return nil
		})
	case CmdMarkOffline:
		r.offlinePending = true
	}
	return nil
}

func (r *Runtime) executeSafely(ctx context.Context, task Task) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("task execution panic: %v\n%s", value, debug.Stack())
		}
	}()
	return r.port.ExecuteTask(ctx, task)
}
func (r *Runtime) handleError(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	metrics.RunTaskErrors.Inc()
	if r.opts.OnError != nil {
		r.opts.OnError(err)
	}
}
