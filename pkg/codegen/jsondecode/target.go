package jsondecode

import (
	"fmt"
	"go/ast"
	"go/parser"

	"github.com/getkin/kin-openapi/openapi3"
)

// target describes a conversion at generation time. Runtime code receives
// concrete types and constant bit widths, never this descriptor.
type target struct {
	Type      string
	Kind      string
	Bits      string
	Converter string
}

func conversionTarget(schema *openapi3.Schema) *target {
	name, _ := schema.Extensions["x-go-type"].(string)
	if name == "" {
		return nil
	}
	t := &target{Type: name}
	switch name {
	case "any", "interface{}", "error":
		return nil
	case "int", "int8", "int16", "int32", "int64", "rune":
		t.Kind = "signed"
		t.Bits = map[string]string{"int": "strconv.IntSize", "int8": "8", "int16": "16", "int32": "32", "int64": "64", "rune": "32"}[name]
	case "uint", "uint8", "uint16", "uint32", "uint64", "byte":
		t.Kind = "unsigned"
		t.Bits = map[string]string{"uint": "strconv.IntSize", "uint8": "8", "uint16": "16", "uint32": "32", "uint64": "64", "byte": "8"}[name]
	case "float32", "float64":
		t.Kind, t.Bits = "float", name[5:]
	case "bool", "string":
		t.Kind = name
	default:
		// Named custom scalar types retain their JSON/TextUnmarshaler. Opaque
		// object/array overrides (such as json.RawMessage) keep existing behavior.
		if schema.Type == nil && len(schema.AnyOf)+len(schema.OneOf)+len(schema.AllOf) == 0 && schema.Extensions["x-go-converter"] == nil {
			return nil
		}
		if schema.Type.Includes("object") || schema.Type.Includes("array") {
			return nil
		}
		expr, err := parser.ParseExpr(name)
		if err != nil {
			return nil
		}
		switch expr.(type) {
		case *ast.Ident, *ast.SelectorExpr:
		default:
			return nil
		}
		t.Kind = "custom"
	}
	t.Converter, _ = schema.Extensions["x-go-converter"].(string)
	return t
}

func validateConverter(schema *openapi3.Schema) error {
	if raw, exists := schema.Extensions["x-go-converter"]; exists {
		name, ok := raw.(string)
		if !ok || name == "" || conversionTarget(schema) == nil {
			return fmt.Errorf("x-go-converter requires a function name and a scalar x-go-type")
		}
	}
	return nil
}

func (r *rule) isScalar() bool { return r.Target != nil }

// IsConvertedMap is shared by type generators so a dictionary's public Go
// value type agrees with its generated decoder. Mixed structs/maps require a
// separate representation and are rejected by the decoder builder.
func IsConvertedMap(file string, schema *openapi3.Schema, resolve Resolver) (bool, error) {
	if schema == nil || len(schema.Properties) != 0 || schema.AdditionalProperties.Schema == nil {
		return false, nil
	}
	return HasConversion(file, schema.AdditionalProperties.Schema, resolve)
}
