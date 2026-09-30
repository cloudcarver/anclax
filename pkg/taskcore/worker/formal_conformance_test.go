//go:build formal

package worker

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
)

// This explores the production Apply function, not a reimplementation of its
// decisions. It is a bounded implementation check, not an unbounded Go proof.
// The oracle counts outstanding work independently of Engine's counters.
func TestFormalEngineConformance(t *testing.T) {
	for _, batch := range []int{0, 2} {
		for _, percentage := range []int32{0, 50, 100} {
			t.Run(fmt.Sprintf("batch_%d_strict_%d", batch, percentage), func(t *testing.T) {
				initial := NewEngine(EngineConfig{Concurrency: 2, ControlConcurrency: 1,
					ClaimBatchSize: batch, MaxStrictPercentage: percentage})
				type node struct {
					engine *Engine
					depth  int
					trace  string
				}
				queue := []node{{engine: initial}}
				seen := map[string]bool{formalEngineKey(initial): true}
				transitions := 0
				for head := 0; head < len(queue); head++ {
					current := queue[head]
					if current.depth == 7 {
						continue
					}
					for _, event := range formalEngineEvents(current.engine) {
						next := formalCloneEngine(current.engine)
						commands := next.Apply(event)
						transitions++
						trace := fmt.Sprintf("%s\n%s cycle=%d tasks=%d", current.trace, event.Type, event.CycleID, len(event.Tasks))
						formalCheckBudget(t, next, trace)
						if current.engine.stopped {
							if formalDrainRank(next) > formalDrainRank(current.engine) {
								t.Fatalf("shutdown rank increased\n%s", trace)
							}
							for _, command := range commands {
								// A previously admitted claim may finish its bounded
								// fallback/group probes using the same reservation.
								if command.Type == CmdClaimNormal {
									old := current.engine.cycles[command.CycleID]
									if old != nil && (old.Phase == PhaseClaimStrict || old.Phase == PhaseClaimNormal) {
										continue
									}
								}
								switch command.Type {
								case CmdClaimBatch, CmdClaimStrict, CmdClaimNormal, CmdClaimControl, CmdClaimByID:
									t.Fatalf("new admission after stop: %+v\n%s", command, trace)
								}
							}
						}
						// Bound exploration identities, never the property checked above.
						if next.nextCycleID > 3 {
							continue
						}
						key := formalEngineKey(next)
						if !seen[key] {
							seen[key] = true
							queue = append(queue, node{next, current.depth + 1, trace})
							if len(queue) > 150000 {
								t.Fatal("exploration exceeded state budget; increase it explicitly")
							}
						}
					}
				}
				t.Logf("production Engine: %d states, %d transitions, depth<=7, cycle IDs<=3", len(seen), transitions)
			})
		}
	}
}

func formalCheckBudget(t *testing.T, e *Engine, trace string) {
	t.Helper()
	business, strict, control := 0, 0, 0
	if e.batch != nil {
		business += e.batch.BatchSize
		strict += e.batch.StrictSlots
	}
	for _, cycle := range e.cycles {
		switch cycle.Lane {
		case LaneControl:
			control++
		case LaneStrict:
			strict++
			business++
		case LaneNormal:
			business++
		default:
			t.Fatalf("unknown lane %q\n%s", cycle.Lane, trace)
		}
	}
	if e.inFlight != business || e.strictInFlight != strict || e.controlInFlight != control ||
		business > e.concurrency || strict > business || control > e.controlConcurrency {
		t.Fatalf("budget mismatch: snapshot=%+v counted=(%d,%d,%d)\n%s", e.Snapshot(), business, strict, control, trace)
	}
}

func formalEngineEvents(e *Engine) []Event {
	events := []Event{{Type: EventPollTick}, {Type: EventControlPollTick}, {Type: EventStop}}
	if e.runtimeConfigVersion == 0 {
		for _, percentage := range []int32{0, 100} {
			value := percentage
			events = append(events, Event{Type: EventRuntimeConfigLoaded,
				Config: &RuntimeConfig{Version: 1, MaxStrictPercentage: &value}})
		}
	}
	if len(e.requests) == 0 {
		for _, task := range []*Task{formalTask(0), formalTask(1), {ID: 1, Spec: apigen.TaskSpec{Type: "prefetchTasks"}}} {
			events = append(events, Event{Type: EventRunTask, TaskID: 1, Task: task, RequestID: "request"})
		}
	} else {
		events = append(events, Event{Type: EventCancelTaskRequest, RequestID: "request"})
	}
	// Completion replay includes already deleted cycles and mismatched phases.
	for id := int64(0); id <= e.nextCycleID; id++ {
		events = append(events, Event{Type: EventExecuteResult, CycleID: id}, Event{Type: EventFinalizeResult, CycleID: id})
	}
	if e.batch != nil {
		for count := 0; count <= e.batch.BatchSize; count++ {
			for strict := 0; strict <= min(count, e.batch.StrictSlots); strict++ {
				tasks := make([]*Task, count)
				for i := range tasks {
					priority := int32(0)
					if i < strict {
						priority = 1
					}
					tasks[i] = formalTask(priority)
					tasks[i].ID = int32(i + 1)
				}
				events = append(events, Event{Type: EventClaimBatchResult, CycleID: e.batch.CycleID, Tasks: tasks})
			}
		}
	}
	// Incorrect batch identities must not release a current reservation.
	events = append(events, Event{Type: EventClaimBatchResult, CycleID: -1})
	ids := make([]int64, 0, len(e.cycles))
	for id := range e.cycles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		cycle := e.cycles[id]
		var kind EventType
		switch cycle.Phase {
		case PhaseClaimStrict:
			kind = EventClaimStrictResult
		case PhaseClaimNormal:
			kind = EventClaimNormalResult
		case PhaseClaimControl:
			kind = EventClaimControlResult
		case PhaseClaimByID:
			kind = EventClaimByIDResult
		default:
			continue
		}
		events = append(events, Event{Type: kind, CycleID: id})
		if cycle.Phase != PhaseClaimStrict {
			events = append(events, Event{Type: kind, CycleID: id, Task: formalTask(0)})
		}
		if cycle.Lane == LaneStrict {
			events = append(events, Event{Type: kind, CycleID: id, Task: formalTask(1)})
		}
	}
	return events
}

func formalTask(priority int32) *Task {
	return &Task{ID: 1, LeaseVersion: 1, Priority: priority, Spec: apigen.TaskSpec{Type: "formal-probe"}}
}

func formalDrainRank(e *Engine) int {
	rank := 0
	if e.batch != nil {
		rank += 3 * e.batch.BatchSize
	}
	for _, cycle := range e.cycles {
		switch cycle.Phase {
		case PhaseExecuting:
			rank += 2
		case PhaseFinalizing:
			rank++
		default:
			rank += 3
		}
	}
	return rank
}

func formalCloneEngine(e *Engine) *Engine {
	clone := *e
	clone.requests = append([]Event(nil), e.requests...)
	clone.cycles = make(map[int64]*cycleState, len(e.cycles))
	for id, cycle := range e.cycles {
		copy := *cycle
		copy.PendingGroups = append([]string(nil), cycle.PendingGroups...)
		copy.WeightedLabels = append([]string(nil), cycle.WeightedLabels...)
		clone.cycles[id] = &copy
	}
	if e.batch != nil {
		batch := *e.batch
		clone.batch = &batch
	}
	return &clone
}

func formalEngineKey(e *Engine) string {
	// Include every mutable decision field, including reservations and queue
	// ordering; Snapshot alone is insufficient for identifying equivalent states.
	state := struct {
		Snapshot Snapshot
		NextID   int64
		Batch    *Command
		Cycles   map[int64]*cycleState
		Requests []Event
	}{e.Snapshot(), e.nextCycleID, e.batch, e.cycles, e.requests}
	raw, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
