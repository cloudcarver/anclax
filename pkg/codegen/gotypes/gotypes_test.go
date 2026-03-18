package gotypes

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

func TestResolvePrimitive(t *testing.T) {
	tests := []struct {
		name    string
		schema  *openapi3.Schema
		goType  string
		imports []string
		ok      bool
	}{
		{
			name:    "uuid string",
			schema:  &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "uuid"},
			goType:  "uuid.UUID",
			imports: []string{"github.com/google/uuid"},
			ok:      true,
		},
		{
			name:    "date time string",
			schema:  &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "date-time"},
			goType:  "time.Time",
			imports: []string{"time"},
			ok:      true,
		},
		{
			name:   "plain string",
			schema: &openapi3.Schema{Type: &openapi3.Types{"string"}},
			goType: "string",
			ok:     true,
		},
		{
			name:   "formatted integer passthrough",
			schema: &openapi3.Schema{Type: &openapi3.Types{"integer"}, Format: "uint32"},
			goType: "uint32",
			ok:     true,
		},
		{
			name:   "default integer",
			schema: &openapi3.Schema{Type: &openapi3.Types{"integer"}},
			goType: "int",
			ok:     true,
		},
		{
			name:   "float number",
			schema: &openapi3.Schema{Type: &openapi3.Types{"number"}, Format: "float"},
			goType: "float32",
			ok:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goType, imports, ok := ResolvePrimitive(tt.schema)
			require.Equal(t, tt.goType, goType)
			require.Equal(t, tt.imports, imports)
			require.Equal(t, tt.ok, ok)
		})
	}
}

func TestPrimitive(t *testing.T) {
	require.Equal(t, "int32", Primitive(&openapi3.Schema{Type: &openapi3.Types{"integer"}, Format: "int32"}))
	require.Equal(t, "float32", Primitive(&openapi3.Schema{Type: &openapi3.Types{"number"}, Format: "float"}))
	require.Equal(t, "string", Primitive(&openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "uuid"}))
}
