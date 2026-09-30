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
			goType, imports, ok, err := ResolvePrimitive(tt.schema)
			require.NoError(t, err)
			require.Equal(t, tt.goType, goType)
			require.Equal(t, tt.imports, imports)
			require.Equal(t, tt.ok, ok)
		})
	}
}

func TestPrimitive(t *testing.T) {
	goType, err := Primitive(&openapi3.Schema{Type: &openapi3.Types{"integer"}, Format: "int32"})
	require.NoError(t, err)
	require.Equal(t, "int32", goType)
	goType, err = Primitive(&openapi3.Schema{Type: &openapi3.Types{"number"}, Format: "float"})
	require.NoError(t, err)
	require.Equal(t, "float32", goType)
	goType, err = Primitive(&openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "uuid"})
	require.NoError(t, err)
	require.Equal(t, "string", goType)
}

func TestRejectsUnknownNumericFormats(t *testing.T) {
	_, _, _, err := ResolvePrimitive(&openapi3.Schema{
		Type:   &openapi3.Types{"integer"},
		Format: "int32; func injected() {}",
	})
	require.ErrorContains(t, err, "unsupported integer format")

}

func TestValidateTypeExpression(t *testing.T) {
	for _, valid := range []string{"json.RawMessage", "[]*example.Value", "map[string]uuid.UUID", "Option[string]", "[16]byte"} {
		require.NoError(t, ValidateTypeExpression(valid), valid)
	}
	for _, invalid := range []string{
		"int; func injected() {}",
		"int // hide generated code",
		"func() string",
		"struct{ Injected string }",
		"chan string",
	} {
		require.Error(t, ValidateTypeExpression(invalid), invalid)
	}
}

func TestValidateIdentifier(t *testing.T) {
	require.NoError(t, ValidateIdentifier("TaskName"))
	require.Error(t, ValidateIdentifier("for"))
	require.Error(t, ValidateIdentifier("123Name"))
	require.Error(t, ValidateIdentifier("_"))
}

func TestEnumLiteralRejectsCodeExpressions(t *testing.T) {
	literal, err := EnumLiteral("int32", -12)
	require.NoError(t, err)
	require.Equal(t, "-12", literal)
	literal, err = EnumLiteral("string", "value\"\nfunc Injected() {}")
	require.NoError(t, err)
	require.Equal(t, `"value\"\nfunc Injected() {}"`, literal)

	_, err = EnumLiteral("int32", "1; func Injected() {}")
	require.Error(t, err)
	_, err = EnumLiteral("float64", "func() {}")
	require.Error(t, err)
}
