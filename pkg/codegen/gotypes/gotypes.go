package gotypes

import (
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
)

func ResolvePrimitive(schema *openapi3.Schema) (string, []string, bool, error) {
	if schema == nil || schema.Type == nil {
		return "", nil, false, nil
	}
	switch {
	case schema.Type.Is("string"):
		switch schema.Format {
		case "uuid":
			return "uuid.UUID", []string{"github.com/google/uuid"}, true, nil
		case "date-time":
			return "time.Time", []string{"time"}, true, nil
		default:
			return "string", nil, true, nil
		}
	case schema.Type.Is("integer"):
		goType, err := Integer(schema.Format)
		return goType, nil, err == nil, err
	case schema.Type.Is("number"):
		goType, err := Number(schema.Format)
		return goType, nil, err == nil, err
	case schema.Type.Is("boolean"):
		return "bool", nil, true, nil
	default:
		return "", nil, false, nil
	}
}

func Primitive(schema *openapi3.Schema) (string, error) {
	if schema == nil || schema.Type == nil {
		return "interface{}", nil
	}
	switch {
	case schema.Type.Is("string"):
		return "string", nil
	case schema.Type.Is("integer"):
		return Integer(schema.Format)
	case schema.Type.Is("number"):
		return Number(schema.Format)
	case schema.Type.Is("boolean"):
		return "bool", nil
	default:
		return "string", nil
	}
}

func Integer(format string) (string, error) {
	supported := map[string]string{
		"":       "int",
		"int":    "int",
		"int8":   "int8",
		"int16":  "int16",
		"int32":  "int32",
		"int64":  "int64",
		"uint":   "uint",
		"uint8":  "uint8",
		"uint16": "uint16",
		"uint32": "uint32",
		"uint64": "uint64",
	}
	goType, ok := supported[format]
	if !ok {
		return "", fmt.Errorf("unsupported integer format %q", format)
	}
	return goType, nil
}

func Number(format string) (string, error) {
	switch format {
	case "float":
		return "float32", nil
	default:
		return "float64", nil
	}
}
