package gotypes

import "github.com/getkin/kin-openapi/openapi3"

func ResolvePrimitive(schema *openapi3.Schema) (string, []string, bool) {
	if schema == nil || schema.Type == nil {
		return "", nil, false
	}
	switch {
	case schema.Type.Is("string"):
		switch schema.Format {
		case "uuid":
			return "uuid.UUID", []string{"github.com/google/uuid"}, true
		case "date-time":
			return "time.Time", []string{"time"}, true
		default:
			return "string", nil, true
		}
	case schema.Type.Is("integer"):
		return Integer(schema.Format), nil, true
	case schema.Type.Is("number"):
		return Number(schema.Format), nil, true
	case schema.Type.Is("boolean"):
		return "bool", nil, true
	default:
		return "", nil, false
	}
}

func Primitive(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil {
		return "interface{}"
	}
	switch {
	case schema.Type.Is("string"):
		return "string"
	case schema.Type.Is("integer"):
		return Integer(schema.Format)
	case schema.Type.Is("number"):
		return Number(schema.Format)
	case schema.Type.Is("boolean"):
		return "bool"
	default:
		return "string"
	}
}

func Integer(format string) string {
	if format != "" {
		return format
	}
	return "int"
}

func Number(format string) string {
	switch format {
	case "float":
		return "float32"
	default:
		return "float64"
	}
}
