package jsondecode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestGenerationRejectsUnsupportedOrAmbiguousRules(t *testing.T) {
	for _, tc := range []struct {
		name, schema, message string
	}{
		{"numeric bound precision", `{"anyOf":[{"type":"integer","maximum":9007199254740993},{"type":"string"}],"x-go-type":"int64"}`, "exact float64 integer range"},
		{"numeric enum precision", `{"anyOf":[{"type":"integer","enum":[9007199254740993]},{"type":"string"}],"x-go-type":"int64"}`, "exact float64 integer range"},
		{"invalid pattern", `{"anyOf":[{"type":"integer"},{"type":"string","pattern":"["}],"x-go-type":"int64"}`, "regexp"},
		{"unresolved branch", `{"anyOf":[{"$ref":"missing.yaml#/schemas/Key"}],"x-go-type":"int64"}`, "cannot resolve"},
		{"inline map", `{"type":"object","properties":{"child":{"type":"object","properties":{"key":{"anyOf":[{"type":"integer"},{"type":"string"}],"x-go-type":"int64"}}}}}`, "named schema"},
		{"mixed additional properties", `{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":{"type":"string","x-go-type":"int64"}}`, "additionalProperties"},
		{"invalid converter", `{"type":"string","x-go-type":"int64","x-go-converter":"func() {}"}`, "x-go-converter"},
		{"converter without target", `{"type":"string","x-go-converter":"ParseKey"}`, "x-go-converter"},
		{"custom type name conflict", `{"type":"string","x-go-type":"Model"}`, "different names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var schema openapi3.Schema
			if err := json.Unmarshal([]byte(tc.schema), &schema); err != nil {
				t.Fatal(err)
			}
			if _, err := Method("spec.yaml", Model{Name: "Model", Underlying: "int64"}, &openapi3.SchemaRef{Value: &schema}, nil, false); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected %q error, got %v", tc.message, err)
			}
		})
	}
}

func TestUnannotatedSchemasKeepExistingDecoding(t *testing.T) {
	for _, data := range []string{
		`{"type":"integer"}`,
		`{"anyOf":[{"type":"integer"},{"type":"string"}]}`,
		`{"type":"object","x-go-type":"json.RawMessage"}`,
		`{"type":"string","x-go-type":"any"}`,
	} {
		var schema openapi3.Schema
		if err := json.Unmarshal([]byte(data), &schema); err != nil {
			t.Fatal(err)
		}
		code, err := Method("spec.yaml", Model{Name: "Model", Underlying: "int64"}, &openapi3.SchemaRef{Value: &schema}, nil, false)
		if err != nil || code.Code != "" {
			t.Fatalf("unannotated model generated a decoder: %q, %v", code, err)
		}
	}
}
