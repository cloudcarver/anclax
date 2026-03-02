package pgnotify

import "encoding/json"

const (
	ChannelRuntimeConfig    = "anclax_worker_runtime_config"
	ChannelRuntimeConfigAck = "anclax_worker_runtime_config_ack"
	ChannelTaskInterrupt    = "anclax_worker_task_interrupt"
	ChannelTaskInterruptAck = "anclax_worker_task_interrupt_ack"
)

const (
	OpUpdateRuntimeConfig = "up_config"
	OpInterruptTask       = "interrupt_task"
	OpAck                 = "ack"
)

type Envelope struct {
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params"`
}

type RuntimeConfigParams struct {
	Version   int64  `json:"version"`
	RequestID string `json:"request_id"`
}

type TaskInterruptParams struct {
	RequestID string `json:"request_id"`
	TaskID    int32  `json:"task_id"`
}

type RuntimeConfigAckParams struct {
	RequestID      string `json:"request_id"`
	WorkerID       string `json:"worker_id"`
	AppliedVersion int64  `json:"applied_version"`
}

type TaskInterruptAckParams struct {
	RequestID string `json:"request_id"`
	WorkerID  string `json:"worker_id"`
}

type RuntimeConfigNotification struct {
	Op     string              `json:"op"`
	Params RuntimeConfigParams `json:"params"`
}

type TaskInterruptNotification struct {
	Op     string              `json:"op"`
	Params TaskInterruptParams `json:"params"`
}

type RuntimeConfigAckNotification struct {
	Op     string                 `json:"op"`
	Params RuntimeConfigAckParams `json:"params"`
}

type TaskInterruptAckNotification struct {
	Op     string                 `json:"op"`
	Params TaskInterruptAckParams `json:"params"`
}

type RuntimeConfigPayload struct {
	MaxStrictPercentage *int32           `json:"maxStrictPercentage,omitempty"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

func ParseEnvelope(payload string) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

func MatchesOp(op string, expected string) bool {
	return op == "" || op == expected
}
