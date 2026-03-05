package taskcoredtmtest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
)

type runtimeHarness struct {
	mu sync.Mutex

	engine     *worker.Engine
	runtime    *worker.Runtime
	port       *deterministicPort
	runtimeCtx context.Context
	cancel     context.CancelFunc

	concurrency   int
	runtimeErrors []string
}

func newRuntimeHarness() *runtimeHarness {
	return &runtimeHarness{}
}

func (h *runtimeHarness) Start(ctx context.Context, concurrency int32, maxStrictPercentage int32) error {
	if concurrency < 1 {
		return fmt.Errorf("concurrency must be >= 1")
	}
	if maxStrictPercentage < 0 || maxStrictPercentage > 100 {
		return fmt.Errorf("maxStrictPercentage must be in [0,100]")
	}

	var (
		prev       *worker.Runtime
		prevCancel context.CancelFunc
	)
	h.mu.Lock()
	prev = h.runtime
	prevCancel = h.cancel

	port := newDeterministicPort()
	engine := worker.NewEngine(worker.EngineConfig{
		WorkerID:            "dtm-worker",
		Concurrency:         int(concurrency),
		MaxStrictPercentage: maxStrictPercentage,
		LabelWeights: map[string]int32{
			worker.DefaultWeightConfigKey: 1,
		},
	})
	opts := worker.DefaultRuntimeOptions()
	opts.OnError = h.recordRuntimeError
	rt := worker.NewRuntime(engine, port, opts)
	runtimeCtx, cancel := context.WithCancel(context.Background())

	h.engine = engine
	h.runtime = rt
	h.port = port
	h.runtimeCtx = runtimeCtx
	h.cancel = cancel
	h.concurrency = int(concurrency)
	h.runtimeErrors = nil
	h.mu.Unlock()

	if prevCancel != nil {
		prevCancel()
	}
	if prev != nil {
		prev.Close()
	}
	return nil
}

func (h *runtimeHarness) Stop(ctx context.Context) error {
	h.mu.Lock()
	rt := h.runtime
	cancel := h.cancel
	h.engine = nil
	h.runtime = nil
	h.port = nil
	h.runtimeCtx = nil
	h.cancel = nil
	h.concurrency = 0
	h.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if rt != nil {
		rt.Close()
	}
	return nil
}

func (h *runtimeHarness) SetExecuteBlocking(ctx context.Context, blocking bool) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	p.setExecuteBlocking(blocking)
	return nil
}

func (h *runtimeHarness) ReleaseExecutions(ctx context.Context, n int32) error {
	if n < 0 {
		return fmt.Errorf("n must be >= 0")
	}
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	p.releaseExecutions(int(n))
	return nil
}

func (h *runtimeHarness) QueueStrictTask(ctx context.Context, task string, priority int32) error {
	if task == "" {
		return fmt.Errorf("task is required")
	}
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	p.queueStrict(taskFromName(task, priority))
	return nil
}

func (h *runtimeHarness) QueueStrictNoTask(ctx context.Context) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	p.queueStrictNoTask()
	return nil
}

func (h *runtimeHarness) QueueNormalTask(ctx context.Context, task string, priority int32) error {
	if task == "" {
		return fmt.Errorf("task is required")
	}
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	p.queueNormal(taskFromName(task, priority))
	return nil
}

func (h *runtimeHarness) QueueNormalNoTask(ctx context.Context) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	p.queueNormalNoTask()
	return nil
}

func (h *runtimeHarness) Poll(ctx context.Context) error {
	_, rt, _, rctx, _, err := h.current()
	if err != nil {
		return err
	}
	rt.Step(rctx, worker.Event{Type: worker.EventPollTick})
	return nil
}

func (h *runtimeHarness) NotifyRuntimeConfig(ctx context.Context, requestID string) error {
	_, rt, _, rctx, _, err := h.current()
	if err != nil {
		return err
	}
	rt.NotifyRuntimeConfig(rctx, requestID)
	return nil
}

func (h *runtimeHarness) SetRefreshConfig(ctx context.Context, requestID string, version int32, maxStrictPercentage int32, defaultWeight int32, w1Weight int32, w2Weight int32) error {
	if requestID == "" {
		return fmt.Errorf("requestID is required")
	}
	if version < 0 {
		return fmt.Errorf("version must be >= 0")
	}
	if maxStrictPercentage < 0 || maxStrictPercentage > 100 {
		return fmt.Errorf("maxStrictPercentage must be in [0,100]")
	}
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}

	weights := map[string]int32{}
	if defaultWeight > 0 {
		weights[worker.DefaultWeightConfigKey] = defaultWeight
	}
	if w1Weight > 0 {
		weights["w1"] = w1Weight
	}
	if w2Weight > 0 {
		weights["w2"] = w2Weight
	}
	cfg := &worker.RuntimeConfig{
		Version:             int64(version),
		MaxStrictPercentage: int32Ptr(maxStrictPercentage),
		LabelWeights:        weights,
	}
	p.setRefreshConfig(requestID, cfg)
	return nil
}

func (h *runtimeHarness) EmitEvent(ctx context.Context, eventType string, cycleID int32, task string, priority int32) error {
	_, rt, _, rctx, _, err := h.current()
	if err != nil {
		return err
	}

	e := worker.Event{CycleID: int64(cycleID)}
	switch eventType {
	case "poll_tick":
		e.Type = worker.EventPollTick
	case "heartbeat_tick":
		e.Type = worker.EventHeartbeatTick
	case "runtime_config_tick":
		e.Type = worker.EventRuntimeConfigTick
	case "stop":
		e.Type = worker.EventStop
	case "claim_strict_result":
		e.Type = worker.EventClaimStrictResult
		if task != "" {
			e.Task = taskFromName(task, priority)
		}
	case "claim_normal_result":
		e.Type = worker.EventClaimNormalResult
		if task != "" {
			e.Task = taskFromName(task, priority)
		}
	case "execute_result":
		e.Type = worker.EventExecuteResult
	case "finalize_result":
		e.Type = worker.EventFinalizeResult
	default:
		return fmt.Errorf("unknown eventType %q", eventType)
	}

	rt.Step(rctx, e)
	return nil
}

func (h *runtimeHarness) SetPortError(ctx context.Context, call string, task string, enabled bool) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	p.setPortError(call, task, enabled)
	return nil
}

func (h *runtimeHarness) ClearPortErrors(ctx context.Context) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	p.clearPortErrors()
	return nil
}

func (h *runtimeHarness) ClearRuntimeErrors(ctx context.Context) error {
	h.mu.Lock()
	h.runtimeErrors = nil
	h.mu.Unlock()
	return nil
}

func (h *runtimeHarness) WaitRuntimeErrorCount(ctx context.Context, expected int32, timeoutMs int32) error {
	if expected < 0 {
		return fmt.Errorf("expected must be >= 0")
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got := h.runtimeErrorCount()
		if got == int(expected) {
			return nil
		}
		if got > int(expected) {
			return fmt.Errorf("runtime error count exceeded: got=%d expected=%d", got, expected)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout waiting runtime error count=%d (got=%d)", expected, h.runtimeErrorCount())
}

func (h *runtimeHarness) AssertRuntimeErrorContains(ctx context.Context, substr string) error {
	errs := h.runtimeErrorsSnapshot()
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return nil
		}
	}
	return fmt.Errorf("runtime error containing %q not found; errors=%v", substr, errs)
}

func (h *runtimeHarness) WaitCallCount(ctx context.Context, call string, expected int32, timeoutMs int32) error {
	if expected < 0 {
		return fmt.Errorf("expected must be >= 0")
	}
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}

	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got := p.callCount(call)
		if got == int(expected) {
			return nil
		}
		if got > int(expected) {
			return fmt.Errorf("call %q exceeded expected: got=%d expected=%d", call, got, expected)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout waiting call count %q=%d (got=%d)", call, expected, p.callCount(call))
}

func (h *runtimeHarness) WaitSnapshot(ctx context.Context, inFlight int32, strictInFlight int32, activeCycles int32, timeoutMs int32) error {
	eng, _, _, _, _, err := h.current()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(durationOrDefault(timeoutMs, time.Second))
	for time.Now().Before(deadline) {
		s := eng.Snapshot()
		if s.InFlight == int(inFlight) && s.StrictInFlight == int(strictInFlight) && s.ActiveCycles == int(activeCycles) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	s := eng.Snapshot()
	return fmt.Errorf("timeout waiting snapshot inFlight=%d strictInFlight=%d activeCycles=%d (got inFlight=%d strictInFlight=%d activeCycles=%d)", inFlight, strictInFlight, activeCycles, s.InFlight, s.StrictInFlight, s.ActiveCycles)
}

func (h *runtimeHarness) AssertOrder(ctx context.Context, stream string, tasks []string) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	got, err := p.stream(stream)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, tasks) {
		return fmt.Errorf("%s order mismatch: got=%v want=%v", stream, got, tasks)
	}
	return nil
}

func (h *runtimeHarness) AssertContains(ctx context.Context, stream string, tasks []string) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	got, err := p.stream(stream)
	if err != nil {
		return err
	}
	if len(got) != len(tasks) {
		return fmt.Errorf("%s size mismatch: got=%v want=%v", stream, got, tasks)
	}
	count := map[string]int{}
	for _, t := range got {
		count[t]++
	}
	for _, t := range tasks {
		if count[t] != 1 {
			return fmt.Errorf("%s missing/duplicate task %q: got=%v want=%v", stream, t, got, tasks)
		}
	}
	return nil
}

func (h *runtimeHarness) AssertAck(ctx context.Context, requestID string, version int32) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	got, ok := p.ackVersion(requestID)
	if !ok {
		return fmt.Errorf("ack for request %q not found", requestID)
	}
	if got != int64(version) {
		return fmt.Errorf("ack version mismatch for %q: got=%d want=%d", requestID, got, version)
	}
	return nil
}

func (h *runtimeHarness) AssertStrictCap(ctx context.Context, expected int32) error {
	eng, _, _, _, _, err := h.current()
	if err != nil {
		return err
	}
	got := eng.Snapshot().StrictCap
	if got != int(expected) {
		return fmt.Errorf("strict cap mismatch: got=%d want=%d", got, expected)
	}
	return nil
}

func (h *runtimeHarness) AssertWeightedLabels(ctx context.Context, labels []string) error {
	eng, _, _, _, _, err := h.current()
	if err != nil {
		return err
	}
	expected := append([]string(nil), labels...)
	sort.Strings(expected)
	got := append([]string(nil), eng.Snapshot().WeightedLabels...)
	sort.Strings(got)
	if !slices.Equal(got, expected) {
		return fmt.Errorf("weighted labels mismatch: got=%v want=%v", got, expected)
	}
	return nil
}

func (h *runtimeHarness) AssertClaimGroupsPrefix(ctx context.Context, groups []string) error {
	_, _, p, _, _, err := h.current()
	if err != nil {
		return err
	}
	got := p.claimGroups()
	if len(got) < len(groups) {
		return fmt.Errorf("claim groups too short: got=%v wantPrefix=%v", got, groups)
	}
	for i := range groups {
		if got[i] != groups[i] {
			return fmt.Errorf("claim groups prefix mismatch at %d: got=%v wantPrefix=%v", i, got, groups)
		}
	}
	return nil
}

func (h *runtimeHarness) AssertInvariants(ctx context.Context) error {
	eng, _, _, _, concurrency, err := h.current()
	if err != nil {
		return err
	}
	s := eng.Snapshot()
	if s.InFlight < 0 || s.StrictInFlight < 0 || s.ActiveCycles < 0 {
		return fmt.Errorf("negative counters in snapshot: %+v", s)
	}
	if s.ActiveCycles != s.InFlight {
		return fmt.Errorf("activeCycles must equal inFlight: %+v", s)
	}
	if s.StrictInFlight > s.InFlight {
		return fmt.Errorf("strictInFlight > inFlight: %+v", s)
	}
	if s.InFlight > concurrency {
		return fmt.Errorf("inFlight > concurrency: snapshot=%+v concurrency=%d", s, concurrency)
	}
	if s.StrictCap < 0 || s.StrictCap > concurrency {
		return fmt.Errorf("strictCap out of range: snapshot=%+v concurrency=%d", s, concurrency)
	}
	return nil
}

func (h *runtimeHarness) AssertStopped(ctx context.Context, expected bool) error {
	eng, _, _, _, _, err := h.current()
	if err != nil {
		return err
	}
	got := eng.Snapshot().Stopped
	if got != expected {
		return fmt.Errorf("stopped mismatch: got=%v want=%v", got, expected)
	}
	return nil
}

func (h *runtimeHarness) StepStop(ctx context.Context) error {
	_, rt, _, rctx, _, err := h.current()
	if err != nil {
		return err
	}
	rt.Step(rctx, worker.Event{Type: worker.EventStop})
	return nil
}

func (h *runtimeHarness) recordRuntimeError(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	h.runtimeErrors = append(h.runtimeErrors, err.Error())
	h.mu.Unlock()
}

func (h *runtimeHarness) runtimeErrorCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runtimeErrors)
}

func (h *runtimeHarness) runtimeErrorsSnapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.runtimeErrors))
	copy(out, h.runtimeErrors)
	return out
}

func (h *runtimeHarness) current() (*worker.Engine, *worker.Runtime, *deterministicPort, context.Context, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.engine == nil || h.runtime == nil || h.port == nil || h.runtimeCtx == nil {
		return nil, nil, nil, nil, 0, fmt.Errorf("runtime harness not started")
	}
	return h.engine, h.runtime, h.port, h.runtimeCtx, h.concurrency, nil
}

type claimResult struct {
	task   *worker.Task
	noTask bool
}

type deterministicPort struct {
	mu sync.Mutex

	strictQueue []claimResult
	normalQueue []claimResult

	execBlocking bool
	execRelease  chan struct{}

	calls map[string]int

	executeOrderLog  []string
	finalizeOrderLog []string
	normalClaimLog   []string

	refreshConfigs map[string]*worker.RuntimeConfig
	ackByRequest   map[string]int64

	globalErr map[string]bool
	taskErr   map[string]map[string]bool
}

func newDeterministicPort() *deterministicPort {
	return &deterministicPort{
		execRelease:    make(chan struct{}, 4096),
		calls:          map[string]int{},
		refreshConfigs: map[string]*worker.RuntimeConfig{},
		ackByRequest:   map[string]int64{},
		globalErr:      map[string]bool{},
		taskErr:        map[string]map[string]bool{},
	}
}

func (p *deterministicPort) RegisterWorker(ctx context.Context, workerID string, labels []string, appliedConfigVersion int64) error {
	p.inc("register")
	return p.injectedErr("register", "")
}

func (p *deterministicPort) MarkWorkerOffline(ctx context.Context, workerID string) error {
	p.inc("mark_offline")
	return p.injectedErr("mark_offline", "")
}

func (p *deterministicPort) ClaimStrict(ctx context.Context, req worker.ClaimRequest) (*worker.Task, error) {
	p.inc("claim_strict")
	if err := p.injectedErr("claim_strict", ""); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.strictQueue) == 0 {
		return nil, worker.ErrNoTask
	}
	out := p.strictQueue[0]
	p.strictQueue = p.strictQueue[1:]
	if out.noTask {
		return nil, worker.ErrNoTask
	}
	return cloneTask(out.task), nil
}

func (p *deterministicPort) ClaimNormalByGroup(ctx context.Context, req worker.ClaimNormalRequest) (*worker.Task, error) {
	p.inc("claim_normal")
	if err := p.injectedErr("claim_normal", ""); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.normalClaimLog = append(p.normalClaimLog, req.Group)
	defer p.mu.Unlock()
	if len(p.normalQueue) == 0 {
		return nil, worker.ErrNoTask
	}
	out := p.normalQueue[0]
	p.normalQueue = p.normalQueue[1:]
	if out.noTask {
		return nil, worker.ErrNoTask
	}
	return cloneTask(out.task), nil
}

func (p *deterministicPort) ClaimByID(ctx context.Context, taskID int32, req worker.ClaimRequest) (*worker.Task, error) {
	p.inc("claim_by_id")
	return nil, worker.ErrNoTask
}

func (p *deterministicPort) ExecuteTask(ctx context.Context, task worker.Task) error {
	name := taskName(task)
	p.inc("execute")
	if err := p.injectedErr("execute", name); err != nil {
		return err
	}
	p.mu.Lock()
	p.executeOrderLog = append(p.executeOrderLog, name)
	block := p.execBlocking
	p.mu.Unlock()

	if block {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.execRelease:
		}
	}
	return nil
}

func (p *deterministicPort) FinalizeTask(ctx context.Context, task worker.Task, execErr error) error {
	name := taskName(task)
	p.inc("finalize")
	p.mu.Lock()
	p.finalizeOrderLog = append(p.finalizeOrderLog, name)
	p.mu.Unlock()
	if err := p.injectedErr("finalize", name); err != nil {
		return err
	}
	return nil
}

func (p *deterministicPort) Heartbeat(ctx context.Context, workerID string) error {
	p.inc("heartbeat")
	return p.injectedErr("heartbeat", "")
}

func (p *deterministicPort) RefreshRuntimeConfig(ctx context.Context, workerID string, requestID string) (*worker.RuntimeConfig, error) {
	p.inc("refresh_config")
	if err := p.injectedErr("refresh_config", requestID); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg := p.refreshConfigs[requestID]
	if cfg == nil {
		return nil, nil
	}
	return cloneRuntimeConfig(cfg), nil
}

func (p *deterministicPort) AckRuntimeConfigApplied(ctx context.Context, workerID string, requestID string, appliedVersion int64) error {
	p.inc("ack_config")
	if err := p.injectedErr("ack_config", requestID); err != nil {
		return err
	}
	p.mu.Lock()
	p.ackByRequest[requestID] = appliedVersion
	p.mu.Unlock()
	return nil
}

func (p *deterministicPort) queueStrict(task *worker.Task) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strictQueue = append(p.strictQueue, claimResult{task: cloneTask(task)})
}

func (p *deterministicPort) queueStrictNoTask() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strictQueue = append(p.strictQueue, claimResult{noTask: true})
}

func (p *deterministicPort) queueNormal(task *worker.Task) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.normalQueue = append(p.normalQueue, claimResult{task: cloneTask(task)})
}

func (p *deterministicPort) queueNormalNoTask() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.normalQueue = append(p.normalQueue, claimResult{noTask: true})
}

func (p *deterministicPort) setExecuteBlocking(blocking bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.execBlocking = blocking
}

func (p *deterministicPort) releaseExecutions(n int) {
	for i := 0; i < n; i++ {
		p.execRelease <- struct{}{}
	}
}

func (p *deterministicPort) setRefreshConfig(requestID string, cfg *worker.RuntimeConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshConfigs[requestID] = cloneRuntimeConfig(cfg)
}

func (p *deterministicPort) setPortError(call string, task string, enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if task == "" {
		if enabled {
			p.globalErr[call] = true
		} else {
			delete(p.globalErr, call)
		}
		return
	}
	m := p.taskErr[call]
	if m == nil {
		m = map[string]bool{}
		p.taskErr[call] = m
	}
	if enabled {
		m[task] = true
	} else {
		delete(m, task)
	}
}

func (p *deterministicPort) clearPortErrors() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.globalErr = map[string]bool{}
	p.taskErr = map[string]map[string]bool{}
}

func (p *deterministicPort) injectedErr(call string, task string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.globalErr[call] {
		return fmt.Errorf("injected %s error", call)
	}
	if task != "" {
		if m := p.taskErr[call]; m != nil && m[task] {
			return fmt.Errorf("injected %s error for task %s", call, task)
		}
	}
	return nil
}

func (p *deterministicPort) callCount(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[name]
}

func (p *deterministicPort) ackVersion(requestID string) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.ackByRequest[requestID]
	return v, ok
}

func (p *deterministicPort) claimGroups() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.normalClaimLog))
	copy(out, p.normalClaimLog)
	return out
}

func (p *deterministicPort) stream(name string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch name {
	case "execute":
		out := make([]string, len(p.executeOrderLog))
		copy(out, p.executeOrderLog)
		return out, nil
	case "finalize":
		out := make([]string, len(p.finalizeOrderLog))
		copy(out, p.finalizeOrderLog)
		return out, nil
	default:
		return nil, fmt.Errorf("unknown stream %q", name)
	}
}

func (p *deterministicPort) inc(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[name]++
}

func taskFromName(name string, priority int32) *worker.Task {
	payload, _ := json.Marshal(map[string]any{"name": name})
	return &worker.Task{
		ID:       int32(time.Now().UnixNano() & 0x7fffffff),
		Priority: priority,
		Attributes: apigen.TaskAttributes{
			Priority: int32Ptr(priority),
		},
		Spec: apigen.TaskSpec{
			Type:    "dtm",
			Payload: payload,
		},
	}
}

func taskName(task worker.Task) string {
	var decoded map[string]any
	if err := json.Unmarshal(task.Spec.Payload, &decoded); err != nil {
		return fmt.Sprintf("id-%d", task.ID)
	}
	if v, ok := decoded["name"].(string); ok && v != "" {
		return v
	}
	return fmt.Sprintf("id-%d", task.ID)
}

func cloneTask(task *worker.Task) *worker.Task {
	if task == nil {
		return nil
	}
	cpy := *task
	cpy.Spec.Payload = append([]byte(nil), task.Spec.Payload...)
	return &cpy
}

func cloneRuntimeConfig(cfg *worker.RuntimeConfig) *worker.RuntimeConfig {
	if cfg == nil {
		return nil
	}
	cpy := *cfg
	if cfg.MaxStrictPercentage != nil {
		v := *cfg.MaxStrictPercentage
		cpy.MaxStrictPercentage = &v
	}
	if cfg.LabelWeights != nil {
		cpy.LabelWeights = map[string]int32{}
		for k, v := range cfg.LabelWeights {
			cpy.LabelWeights[k] = v
		}
	}
	return &cpy
}

func durationOrDefault(ms int32, fallback time.Duration) time.Duration {
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func int32Ptr(v int32) *int32 { return &v }

var _ worker.Port = (*deterministicPort)(nil)
