package worker

import (
	"context"
	"sort"
)

type Engine struct {
	workerID string
	labels   []string

	concurrency        int
	controlConcurrency int
	controlInFlight    int
	requests           []Event

	stopped bool

	inFlight       int
	strictInFlight int
	strictCap      int

	runtimeConfigVersion int64
	maxStrictPercentage  int32
	weightedLabels       []string
	normalClaimWheel     []string
	normalClaimCursor    int

	nextCycleID int64
	cycles      map[int64]*cycleState
}

func NewEngine(cfg EngineConfig) *Engine {
	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}

	e := &Engine{
		workerID:           cfg.WorkerID,
		labels:             append([]string(nil), cfg.Labels...),
		concurrency:        concurrency,
		controlConcurrency: max(0, cfg.ControlConcurrency),
		cycles:             map[int64]*cycleState{},
	}

	defaultStrict := cfg.MaxStrictPercentage
	e.applyRuntimeConfig(RuntimeConfig{
		Version:             0,
		MaxStrictPercentage: &defaultStrict,
		LabelWeights:        cfg.LabelWeights,
	})
	return e
}

// WorkerID returns immutable worker identity configured at engine creation.
func (e *Engine) WorkerID() string {
	return e.workerID
}

// Labels returns immutable worker labels configured at engine creation.
func (e *Engine) Labels() []string {
	return append([]string(nil), e.labels...)
}

// CurrentRuntimeConfigVersion reads mutable engine state.
// Caller must ensure single-owner access (same owner as Apply).
func (e *Engine) CurrentRuntimeConfigVersion() int64 {
	return e.runtimeConfigVersion
}

// Snapshot reads mutable engine state.
// Caller must ensure single-owner access (same owner as Apply).
func (e *Engine) Snapshot() Snapshot {
	return Snapshot{
		ControlInFlight:        e.controlInFlight,
		PendingRequests:        len(e.requests),
		WorkerID:               e.workerID,
		Stopped:                e.stopped,
		InFlight:               e.inFlight,
		StrictInFlight:         e.strictInFlight,
		StrictCap:              e.strictCap,
		RuntimeConfigVersion:   e.runtimeConfigVersion,
		MaxStrictPercentage:    e.maxStrictPercentage,
		WeightedLabels:         append([]string(nil), e.weightedLabels...),
		NormalClaimWheel:       append([]string(nil), e.normalClaimWheel...),
		NormalClaimWheelCursor: e.normalClaimCursor,
		ActiveCycles:           len(e.cycles),
	}
}

// Apply mutates engine state and must be called by a single owner goroutine
// (runtime event loop).
func (e *Engine) Apply(event Event) []Command {
	switch event.Type {
	case EventControlPollTick:
		return e.onControlPollTick()
	case EventClaimControlResult, EventClaimByIDResult:
		return e.onDirectClaimResult(event)
	case EventRunTask:
		if e.stopped {
			return []Command{{Type: CmdTaskRequestDone, RequestID: event.RequestID, Err: context.Canceled}}
		}
		e.requests = append(e.requests, event)
		return e.admitRequests()
	case EventCancelTaskRequest:
		for i, req := range e.requests {
			if req.RequestID == event.RequestID {
				e.requests = append(e.requests[:i], e.requests[i+1:]...)
				return []Command{{Type: CmdTaskRequestDone, RequestID: event.RequestID, Err: context.Canceled}}
			}
		}
		return nil
	case EventPollTick:
		return e.onPollTick()
	case EventClaimStrictResult:
		return e.onClaimStrictResult(event)
	case EventClaimNormalResult:
		return e.onClaimNormalResult(event)
	case EventExecuteResult:
		return e.onExecuteResult(event)
	case EventFinalizeResult:
		return e.onFinalizeResult(event)
	case EventHeartbeatTick:
		if e.stopped {
			return nil
		}
		return []Command{{Type: CmdHeartbeat}}
	case EventRuntimeConfigTick, EventRuntimeConfigNotify:
		if e.stopped {
			return nil
		}
		return []Command{{Type: CmdRefreshRuntimeConfig, RequestID: event.RequestID}}
	case EventRuntimeConfigLoaded:
		if event.Err != nil {
			return nil
		}
		var commands []Command
		if event.Config != nil && event.Config.Version > e.runtimeConfigVersion {
			e.applyRuntimeConfig(*event.Config)
			commands = e.admitRequests()
		}
		if event.Config != nil || event.RequestID != "" {
			commands = append(commands, Command{
				Type:           CmdAckRuntimeConfig,
				RequestID:      event.RequestID,
				AppliedVersion: e.runtimeConfigVersion,
			})
		}
		return commands
	case EventStop:
		if e.stopped {
			return nil
		}
		e.stopped = true
		commands := []Command{{Type: CmdMarkOffline}}
		for _, req := range e.requests {
			commands = append(commands, Command{Type: CmdTaskRequestDone, RequestID: req.RequestID, Err: context.Canceled})
		}
		e.requests = nil
		return commands
	default:
		return nil
	}
}

func (e *Engine) onPollTick() []Command {
	if e.stopped || e.inFlight >= e.concurrency {
		return nil
	}

	e.nextCycleID++
	cycleID := e.nextCycleID
	e.inFlight++

	if e.strictInFlight < e.strictCap {
		e.strictInFlight++
		e.cycles[cycleID] = &cycleState{
			ID:    cycleID,
			Lane:  LaneStrict,
			Phase: PhaseClaimStrict,
		}
		return []Command{{Type: CmdClaimStrict, CycleID: cycleID}}
	}

	groups, weighted := e.nextNormalClaimGroups()
	cycle := &cycleState{
		ID:             cycleID,
		Lane:           LaneNormal,
		Phase:          PhaseClaimNormal,
		PendingGroups:  groups,
		WeightedLabels: weighted,
	}
	e.cycles[cycleID] = cycle
	return e.issueNextNormalClaim(cycle)
}

func (e *Engine) onClaimStrictResult(event Event) []Command {
	cycle, ok := e.cycles[event.CycleID]
	if !ok {
		return nil
	}
	if cycle.Phase != PhaseClaimStrict {
		return nil
	}

	if event.Err != nil {
		return e.finishCycleResult(event.CycleID, event.Err)
	}

	if event.Task == nil {
		groups, weighted := e.nextNormalClaimGroups()
		// Keep the strict reservation until the fallback claim completes. The
		// database rechecks strict tasks that arrived after the first query.
		cycle.Phase = PhaseClaimNormal
		cycle.PendingGroups = groups
		cycle.WeightedLabels = weighted
		return e.issueNextNormalClaim(cycle)
	}

	cycle.Task = copyTask(event.Task)
	cycle.Phase = PhaseExecuting
	return append([]Command{{Type: CmdExecuteTask, CycleID: cycle.ID, Task: copyTask(cycle.Task), RequestID: cycle.RequestID}}, e.admitRequests()...)
}

func (e *Engine) onClaimNormalResult(event Event) []Command {
	cycle, ok := e.cycles[event.CycleID]
	if !ok {
		return nil
	}
	if cycle.Phase != PhaseClaimNormal {
		return nil
	}
	if event.Err != nil {
		return e.finishCycleResult(event.CycleID, event.Err)
	}
	if event.Task == nil {
		return e.issueNextNormalClaim(cycle)
	}
	if cycle.Lane == LaneStrict && event.Task.Priority == 0 {
		cycle.Lane = LaneNormal
		e.strictInFlight--
	}
	cycle.Task = copyTask(event.Task)
	cycle.Phase = PhaseExecuting
	return append([]Command{{Type: CmdExecuteTask, CycleID: cycle.ID, Task: copyTask(cycle.Task), RequestID: cycle.RequestID}}, e.admitRequests()...)
}

func (e *Engine) onExecuteResult(event Event) []Command {
	cycle, ok := e.cycles[event.CycleID]
	if !ok {
		return nil
	}
	if cycle.Phase != PhaseExecuting {
		return nil
	}
	if cycle.Task == nil {
		return e.finishCycleResult(event.CycleID, event.Err)
	}
	cycle.Phase = PhaseFinalizing
	return []Command{{
		Type:      CmdFinalize,
		CycleID:   cycle.ID,
		Task:      copyTask(cycle.Task),
		ExecErr:   event.ExecErr,
		RequestID: cycle.RequestID,
	}}
}

func (e *Engine) onFinalizeResult(event Event) []Command {
	cycle, ok := e.cycles[event.CycleID]
	if !ok {
		return nil
	}
	if cycle.Phase != PhaseFinalizing {
		return nil
	}
	return e.finishCycleResult(event.CycleID, event.Err)
}

func (e *Engine) issueNextNormalClaim(cycle *cycleState) []Command {
	if len(cycle.PendingGroups) == 0 {
		return e.finishCycleResult(cycle.ID, nil)
	}
	group := cycle.PendingGroups[0]
	cycle.PendingGroups = cycle.PendingGroups[1:]
	return []Command{{
		Type:           CmdClaimNormal,
		AllowStrict:    cycle.Lane == LaneStrict,
		CycleID:        cycle.ID,
		Group:          group,
		WeightedLabels: append([]string(nil), cycle.WeightedLabels...),
	}}
}

func (e *Engine) finishCycleResult(cycleID int64, err error) []Command {
	cycle, ok := e.cycles[cycleID]
	if !ok {
		return nil
	}
	switch cycle.Lane {
	case LaneControl:
		e.controlInFlight--
	case LaneStrict:
		e.strictInFlight--
		e.inFlight--
	default:
		e.inFlight--
	}
	delete(e.cycles, cycleID)
	var commands []Command
	if cycle.RequestID != "" {
		commands = append(commands, Command{Type: CmdTaskRequestDone, RequestID: cycle.RequestID, Err: err})
	}
	return append(commands, e.admitRequests()...)
}

func (e *Engine) onControlPollTick() []Command {
	if e.stopped || e.controlInFlight >= e.controlConcurrency {
		return nil
	}
	e.nextCycleID++
	cycle := &cycleState{ID: e.nextCycleID, Lane: LaneControl, Phase: PhaseClaimControl}
	e.cycles[cycle.ID] = cycle
	e.controlInFlight++
	return []Command{{Type: CmdClaimControl, CycleID: cycle.ID}}
}

func (e *Engine) onDirectClaimResult(event Event) []Command {
	cycle := e.cycles[event.CycleID]
	if cycle == nil || (cycle.Phase != PhaseClaimControl && cycle.Phase != PhaseClaimByID) {
		return nil
	}
	if event.Err != nil || event.Task == nil {
		return e.finishCycleResult(event.CycleID, event.Err)
	}
	if cycle.Lane == LaneStrict && event.Task.Priority == 0 {
		cycle.Lane = LaneNormal
		e.strictInFlight--
	}
	cycle.Task, cycle.Phase = copyTask(event.Task), PhaseExecuting
	return append([]Command{{Type: CmdExecuteTask, CycleID: cycle.ID, Task: copyTask(cycle.Task), RequestID: cycle.RequestID}}, e.admitRequests()...)
}

func (e *Engine) admitRequests() []Command {
	if e.stopped {
		return nil
	}
	var commands []Command
	pending := e.requests[:0]
	for _, req := range e.requests {
		lane := LaneNormal
		if req.Task != nil && IsControlTask(req.Task.GetType()) && e.controlConcurrency > 0 {
			lane = LaneControl
			if e.controlInFlight >= e.controlConcurrency {
				pending = append(pending, req)
				continue
			}
		} else {
			if e.inFlight >= e.concurrency {
				pending = append(pending, req)
				continue
			}
			if req.Task != nil && req.Task.Priority > 0 {
				if e.strictCap == 0 {
					commands = append(commands, Command{Type: CmdTaskRequestDone, RequestID: req.RequestID})
					continue
				}
				if e.strictInFlight >= e.strictCap {
					pending = append(pending, req)
					continue
				}
				lane = LaneStrict
			}
		}
		e.nextCycleID++
		cycle := &cycleState{ID: e.nextCycleID, Lane: lane, Phase: PhaseClaimByID, RequestID: req.RequestID}
		e.cycles[cycle.ID] = cycle
		if lane == LaneControl {
			e.controlInFlight++
		} else {
			e.inFlight++
			if lane == LaneStrict {
				e.strictInFlight++
			}
		}
		commands = append(commands, Command{Type: CmdClaimByID, CycleID: cycle.ID, TaskID: req.TaskID, RequestID: req.RequestID, AllowStrict: lane != LaneNormal})
	}
	e.requests = pending
	return commands
}

func (e *Engine) nextNormalClaimGroups() ([]string, []string) {
	weighted := append([]string(nil), e.weightedLabels...)
	if len(e.normalClaimWheel) == 0 {
		return []string{DefaultWeightGroup}, weighted
	}
	start := e.normalClaimCursor
	e.normalClaimCursor = (e.normalClaimCursor + 1) % len(e.normalClaimWheel)

	groups := make([]string, 0, len(e.normalClaimWheel))
	seen := make(map[string]struct{}, len(e.normalClaimWheel))
	for i := 0; i < len(e.normalClaimWheel); i++ {
		group := e.normalClaimWheel[(start+i)%len(e.normalClaimWheel)]
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		groups = append(groups, group)
	}
	return groups, weighted
}

func (e *Engine) applyRuntimeConfig(cfg RuntimeConfig) {
	percentage := int32(100)
	if cfg.MaxStrictPercentage != nil {
		percentage = *cfg.MaxStrictPercentage
		if percentage < 0 {
			percentage = 0
		}
		if percentage > 100 {
			percentage = 100
		}
	}

	weights := map[string]int32{}
	for k, v := range cfg.LabelWeights {
		if v < 1 {
			continue
		}
		if k == DefaultWeightConfigKey {
			k = DefaultWeightGroup
		}
		weights[k] = v
	}
	if _, ok := weights[DefaultWeightGroup]; !ok {
		weights[DefaultWeightGroup] = 1
	}

	weighted := make([]string, 0, len(weights))
	for label := range weights {
		if label == DefaultWeightGroup {
			continue
		}
		weighted = append(weighted, label)
	}
	sort.Strings(weighted)

	wheel := buildClaimWheel(weights)
	if len(wheel) == 0 {
		wheel = []string{DefaultWeightGroup}
	}

	e.runtimeConfigVersion = cfg.Version
	e.maxStrictPercentage = percentage
	e.strictCap = strictCapForPercentage(e.concurrency, percentage)
	e.weightedLabels = weighted
	e.normalClaimWheel = wheel
	if len(wheel) > 0 {
		e.normalClaimCursor = e.normalClaimCursor % len(wheel)
	} else {
		e.normalClaimCursor = 0
	}
}

func buildClaimWheel(weights map[string]int32) []string {
	groups := make([]string, 0, len(weights))
	for group := range weights {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	wheel := make([]string, 0, len(groups))
	for _, group := range groups {
		weight := weights[group]
		for i := int32(0); i < weight; i++ {
			wheel = append(wheel, group)
		}
	}
	return wheel
}

func strictCapForPercentage(concurrency int, percentage int32) int {
	if concurrency <= 0 || percentage <= 0 {
		return 0
	}
	if percentage >= 100 {
		return concurrency
	}
	cap := (concurrency*int(percentage) + 99) / 100
	if cap < 1 {
		return 1
	}
	if cap > concurrency {
		return concurrency
	}
	return cap
}

func copyTask(task *Task) *Task {
	if task == nil {
		return nil
	}
	clone := *task
	clone.Attributes = task.Attributes
	clone.Spec = task.Spec
	clone.Spec.Payload = append([]byte(nil), task.Spec.Payload...)
	return &clone
}
