package workerv2

import "sort"

type Engine struct {
	workerID  string
	labels    []string
	hasLabels bool

	concurrency int

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
		workerID:    cfg.WorkerID,
		labels:      append([]string(nil), cfg.Labels...),
		hasLabels:   len(cfg.Labels) > 0,
		concurrency: concurrency,
		cycles:      map[int64]*cycleState{},
	}

	defaultStrict := cfg.MaxStrictPercentage
	e.applyRuntimeConfig(RuntimeConfig{
		Version:             0,
		MaxStrictPercentage: &defaultStrict,
		LabelWeights:        cfg.LabelWeights,
	})
	return e
}

func (e *Engine) WorkerID() string {
	return e.workerID
}

func (e *Engine) Labels() []string {
	return append([]string(nil), e.labels...)
}

func (e *Engine) CurrentRuntimeConfigVersion() int64 {
	return e.runtimeConfigVersion
}

func (e *Engine) Snapshot() Snapshot {
	return Snapshot{
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

func (e *Engine) Apply(event Event) []Command {
	switch event.Type {
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
		if event.Config != nil && event.Config.Version > e.runtimeConfigVersion {
			e.applyRuntimeConfig(*event.Config)
		}
		if event.Config != nil || event.RequestID != "" {
			return []Command{{
				Type:           CmdAckRuntimeConfig,
				RequestID:      event.RequestID,
				AppliedVersion: e.runtimeConfigVersion,
			}}
		}
		return nil
	case EventStop:
		if e.stopped {
			return nil
		}
		e.stopped = true
		return []Command{{Type: CmdMarkOffline}}
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
		e.finishCycle(event.CycleID)
		return nil
	}

	if event.Task == nil {
		if e.strictInFlight > 0 {
			e.strictInFlight--
		}
		groups, weighted := e.nextNormalClaimGroups()
		cycle.Lane = LaneNormal
		cycle.Phase = PhaseClaimNormal
		cycle.PendingGroups = groups
		cycle.WeightedLabels = weighted
		return e.issueNextNormalClaim(cycle)
	}

	cycle.Task = copyTask(event.Task)
	cycle.Phase = PhaseExecuting
	return []Command{{Type: CmdExecuteTask, CycleID: cycle.ID, Task: copyTask(cycle.Task)}}
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
		e.finishCycle(event.CycleID)
		return nil
	}
	if event.Task == nil {
		return e.issueNextNormalClaim(cycle)
	}
	cycle.Task = copyTask(event.Task)
	cycle.Phase = PhaseExecuting
	return []Command{{Type: CmdExecuteTask, CycleID: cycle.ID, Task: copyTask(cycle.Task)}}
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
		e.finishCycle(event.CycleID)
		return nil
	}
	cycle.Phase = PhaseFinalizing
	return []Command{{
		Type:    CmdFinalize,
		CycleID: cycle.ID,
		Task:    copyTask(cycle.Task),
		ExecErr: event.ExecErr,
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
	e.finishCycle(event.CycleID)
	return nil
}

func (e *Engine) issueNextNormalClaim(cycle *cycleState) []Command {
	if len(cycle.PendingGroups) == 0 {
		e.finishCycle(cycle.ID)
		return nil
	}
	group := cycle.PendingGroups[0]
	cycle.PendingGroups = cycle.PendingGroups[1:]
	return []Command{{
		Type:           CmdClaimNormal,
		CycleID:        cycle.ID,
		Group:          group,
		WeightedLabels: append([]string(nil), cycle.WeightedLabels...),
	}}
}

func (e *Engine) finishCycle(cycleID int64) {
	cycle, ok := e.cycles[cycleID]
	if !ok {
		return
	}
	if cycle.Lane == LaneStrict && e.strictInFlight > 0 {
		e.strictInFlight--
	}
	if e.inFlight > 0 {
		e.inFlight--
	}
	delete(e.cycles, cycleID)
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
