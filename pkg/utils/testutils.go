package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

type JSONValueMatcher struct {
	v []byte
}

func NewJSONValueMatcher(t *testing.T, v any) *JSONValueMatcher {
	jsonData, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal value: %v", err)
	}

	return &JSONValueMatcher{v: jsonData}
}

func (m *JSONValueMatcher) Matches(x any) bool {
	jsonData, err := json.Marshal(x)
	if err != nil {
		return false
	}

	return bytes.Equal(jsonData, m.v)
}

func (m *JSONValueMatcher) String() string {
	return fmt.Sprintf("is equal to %s", string(m.v))
}
