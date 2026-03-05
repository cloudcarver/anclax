package worker

import (
	"context"
	"encoding/json"
	"errors"

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
	PhaseClaimStrict Phase = "claim_strict"
	PhaseClaimNormal Phase = "claim_normal"
	PhaseExecuting   Phase = "executing"
	PhaseFinalizing  Phase = "finalizing"
)

type Lane string

const (
	LaneStrict Lane = "strict"
	LaneNormal Lane = "normal"
)

type EventType string

const (
	EventPollTick EventType = "poll_tick"

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
	CmdClaimStrict CommandType = "claim_strict"
	CmdClaimNormal CommandType = "claim_normal"
	CmdExecuteTask CommandType = "execute_task"
	CmdFinalize    CommandType = "finalize"

	CmdHeartbeat CommandType = "heartbeat"

	CmdRefreshRuntimeConfig CommandType = "refresh_runtime_config"
	CmdAckRuntimeConfig     CommandType = "ack_runtime_config"
	CmdMarkOffline          CommandType = "mark_offline"
)

type Task struct {
	ID         int32
	Priority   int32
	Attempts   int32
	Attributes apigen.TaskAttributes
	Spec       apigen.TaskSpec
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
	Type      EventType
	CycleID   int64
	Task      *Task
	ExecErr   error
	Err       error
	RequestID string
	Config    *RuntimeConfig
}

type Command struct {
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
	WorkerID  string
	Labels    []string
	HasLabels bool
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

	Heartbeat(ctx context.Context, workerID string) error
	RefreshRuntimeConfig(ctx context.Context, workerID string, requestID string) (*RuntimeConfig, error)
	AckRuntimeConfigApplied(ctx context.Context, workerID string, requestID string, appliedVersion int64) error
}

type EngineConfig struct {
	WorkerID            string
	Labels              []string
	Concurrency         int
	MaxStrictPercentage int32
	LabelWeights        map[string]int32
}

type Snapshot struct {
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
	ID             int64
	Lane           Lane
	Phase          Phase
	Task           *Task
	PendingGroups  []string
	WeightedLabels []string
}
