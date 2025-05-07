package apigen

import "encoding/json"

func (t *TaskSpec) GetType() string {
	return t.Type
}

func (t *TaskSpec) GetPayload() json.RawMessage {
	return t.Payload
}
