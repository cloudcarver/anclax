package apigen

import "encoding/json"

func (t *TaskSpec) GetPayload() json.RawMessage {
	return t.Payload
}

func (t *TaskSpec) GetType() string {
	return t.Type
}
