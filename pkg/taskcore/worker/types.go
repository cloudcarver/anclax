package worker

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
)

var (
	ErrNoTask = errors.New("no task available")
)

const (
	DefaultWeightGroup     = "__default__"
	DefaultWeightConfigKey = "default"
)

type Phase string

const (
	PhaseClaimControl Phase = "claim_control"
	PhaseClaimByID    Phase = "claim_by_id"
	PhaseClaimStrict  Phase = "claim_strict"
	PhaseClaimNormal  Phase = "claim_normal"
	PhaseExecuting    Phase = "executing"
	PhaseFinalizing   Phase = "finalizing"
)

type Lane string

const (
	LaneControl Lane = "control"
	LaneStrict  Lane = "strict"
	LaneNormal  Lane = "normal"
)

type EventType string

const (
	EventControlPollTick    EventType = "control_poll_tick"
	EventClaimControlResult EventType = "claim_control_result"
	EventRunTask            EventType = "run_task"
	EventCancelTaskRequest  EventType = "cancel_task_request"
	EventClaimByIDResult    EventType = "claim_by_id_result"
	EventPollTick           EventType = "poll_tick"
	EventClaimBatchResult   EventType = "claim_batch_result"

	EventClaimStrictResult EventType = "claim_strict_result"
	EventClaimNormalResult EventType = "claim_normal_result"
	EventExecuteResult     EventType = "execute_result"
	EventFinalizeResult    EventType = "finalize_result"

	EventHeartbeatTick EventType = "heartbeat_tick"

	EventRuntimeConfigTick   EventType = "runtime_config_tick"
	EventRuntimeConfigNotify EventType = "runtime_config_notify"
	EventRuntimeConfigLoaded EventType = "runtime_config_loaded"

	EventStop EventType = "stop"
)

type CommandType string

const (
	CmdClaimControl    CommandType = "claim_control"
	CmdClaimByID       CommandType = "claim_by_id"
	CmdTaskRequestDone CommandType = "task_request_done"
	CmdClaimStrict     CommandType = "claim_strict"
	CmdClaimNormal     CommandType = "claim_normal"
	CmdClaimBatch      CommandType = "claim_batch"
	CmdExecuteTask     CommandType = "execute_task"
	CmdFinalize        CommandType = "finalize"

	CmdHeartbeat CommandType = "heartbeat"

	CmdRefreshRuntimeConfig CommandType = "refresh_runtime_config"
	CmdAckRuntimeConfig     CommandType = "ack_runtime_config"
	CmdMarkOffline          CommandType = "mark_offline"
)

type Task struct {
	ID           int32
	LeaseVersion int64
	claimedAt    time.Time
	Priority     int32
	Attempts     int32
	Attributes   apigen.TaskAttributes
	Spec         apigen.TaskSpec
}

func (t *Task) GetType() string {
	if t == nil {
		return ""
	}
	return t.Spec.Type
}

func (t *Task) GetPayload() json.RawMessage {
	if t == nil {
		return nil
	}
	return t.Spec.Payload
}

type RuntimeConfig struct {
	Version             int64
	MaxStrictPercentage *int32
	LabelWeights        map[string]int32
}

type Event struct {
	Tasks     []*Task
	TaskID    int32
	Type      EventType
	CycleID   int64
	Task      *Task
	ExecErr   error
	Err       error
	RequestID string
	Config    *RuntimeConfig
}

type Command struct {
	BatchSize      int
	StrictSlots    int
	Groups         []string
	TaskID         int32
	AllowStrict    bool
	Err            error
	Type           CommandType
	CycleID        int64
	Task           *Task
	ExecErr        error
	Group          string
	WeightedLabels []string
	RequestID      string
	AppliedVersion int64
}

type ClaimRequest struct {
	AllowStrict bool
	WorkerID    string
	Labels      []string
	HasLabels   bool
}

type ClaimNormalRequest struct {
	ClaimRequest
	Group          string
	WeightedLabels []string
}

type Port interface {
	RegisterWorker(ctx context.Context, workerID string, labels []string, appliedConfigVersion int64) error
	MarkWorkerOffline(ctx context.Context, workerID string) error

	ClaimStrict(ctx context.Context, req ClaimRequest) (*Task, error)
	ClaimNormalByGroup(ctx context.Context, req ClaimNormalRequest) (*Task, error)
	ClaimByID(ctx context.Context, taskID int32, req ClaimRequest) (*Task, error)

	ExecuteTask(ctx context.Context, task Task) error
	FinalizeTask(ctx context.Context, task Task, execErr error) error
	InterruptTask(taskID int32, cause error)
	WaitTaskRuntimes(ctx context.Context, taskIDs []int32) error

	Heartbeat(ctx context.Context, workerID string) error
	RefreshRuntimeConfig(ctx context.Context, workerID string, requestID string) (*RuntimeConfig, error)
	AckRuntimeConfigApplied(ctx context.Context, workerID string, requestID string, appliedVersion int64) error
}

type RuntimeConfigPayload struct {
	MaxStrictPercentage *int32           `json:"maxStrictPercentage,omitempty"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type EngineConfig struct {
	ClaimBatchSize      int
	ControlConcurrency  int
	WorkerID            string
	Labels              []string
	Concurrency         int
	MaxStrictPercentage int32
	LabelWeights        map[string]int32
}

type Snapshot struct {
	Claiming               int
	Executing              int
	Finalizing             int
	ControlInFlight        int
	PendingRequests        int
	WorkerID               string
	Stopped                bool
	InFlight               int
	StrictInFlight         int
	StrictCap              int
	RuntimeConfigVersion   int64
	MaxStrictPercentage    int32
	WeightedLabels         []string
	NormalClaimWheel       []string
	NormalClaimWheelCursor int
	ActiveCycles           int
}

type cycleState struct {
	RequestID      string
	ID             int64
	Lane           Lane
	Phase          Phase
	Task           *Task
	PendingGroups  []string
	WeightedLabels []string
}

// BatchPort enables one bounded automatic claim per worker. The legacy Port
// remains available to deterministic adapters and manual single-task callers.
type BatchPort interface {
	ClaimBatch(context.Context, ClaimBatchRequest) ([]*Task, error)
}

type ClaimBatchRequest struct {
	BatchSize      int
	StrictSlots    int
	Groups         []string
	WeightedLabels []string
}

// These optional ports let existing deterministic adapters keep using the base
// Port while production workers enable a separately budgeted control lane.
type ControlPort interface {
	ClaimControl(context.Context, ClaimRequest) (*Task, error)
}
type TaskLookupPort interface {
	LookupTask(context.Context, int32) (*Task, error)
}

// TaskRuntimesObserver lets control handlers check convergence without occupying
// an execution slot while waiting. WorkerInterface remains source compatible.
type TaskRuntimesObserver interface{ TaskRuntimesActive([]int32) bool }

// IsControlTask identifies the framework worker-control protocol. Keep this list
// aligned with ClaimWorkerCommand and the built-in task spec.
func IsControlTask(taskType string) bool {
	switch taskType {
	case "broadcastUpdateWorkerRuntimeConfig", "applyWorkerRuntimeConfigToWorker", "broadcastCancelTask", "cancelTaskOnWorker", "broadcastPauseTask", "pauseTaskOnWorker":
		return true
	default:
		return false
	}
}
