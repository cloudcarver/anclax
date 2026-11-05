// Package jsondecode implements JSON decoding rules emitted by Anclax generators.
package jsondecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Rule describes only the parts of a generated model that need int64 conversion.
// Delegate leaves decoding to a referenced generated type's UnmarshalJSON method.
type Rule struct {
	Kind       string           `json:"kind"`
	Nullable   bool             `json:"nullable,omitempty"`
	Delegate   bool             `json:"delegate,omitempty"`
	Schema     *openapi3.Schema `json:"schema,omitempty"`
	Properties map[string]Field `json:"properties,omitempty"`
	Items      *Rule            `json:"items,omitempty"`
}

type Field struct {
	Rule     *Rule `json:"rule"`
	Required bool  `json:"required,omitempty"`
}

// Decoder is immutable and safe for concurrent use.
type Decoder struct{ rule Rule }

// Compile checks a generated decoding rule before it is used at runtime.
func Compile(data string) (*Decoder, error) {
	var rule Rule
	if err := json.Unmarshal([]byte(data), &rule); err != nil {
		return nil, err
	}
	if err := checkRule(&rule); err != nil {
		return nil, err
	}
	return &Decoder{rule: rule}, nil
}

// MustCompile is used for rules that have already been checked during generation.
func MustCompile(data string) *Decoder {
	d, err := Compile(data)
	if err != nil {
		panic(err)
	}
	return d
}

func checkRule(rule *Rule) error {
	if rule == nil {
		return fmt.Errorf("missing JSON decoding rule")
	}
	if rule.Delegate {
		return nil
	}
	switch rule.Kind {
	case "int64":
		if rule.Schema == nil {
			return fmt.Errorf("missing int64 input schema")
		}
		return rule.Schema.Validate(context.Background())
	case "object":
		for name, field := range rule.Properties {
			if err := checkRule(field.Rule); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case "array":
		return checkRule(rule.Items)
	default:
		return fmt.Errorf("unsupported JSON decoding rule %q", rule.Kind)
	}
	return nil
}

// Unmarshal validates and normalizes annotated values before decoding into dst.
// Generated callers pass a local defined type to avoid invoking themselves again.
func (d *Decoder) Unmarshal(data []byte, dst any) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid JSON")
	}
	normalized, err := normalize(data, &d.rule, "$", false)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, dst)
}

func normalize(data []byte, rule *Rule, path string, optional bool) ([]byte, error) {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		if !rule.Nullable {
			return nil, fmt.Errorf("%s: null is not allowed", path)
		}
		if rule.Kind == "int64" && !optional {
			return nil, fmt.Errorf("%s: null cannot be assigned to int64", path)
		}
		return data, nil
	}
	if rule.Delegate {
		return data, nil
	}
	switch rule.Kind {
	case "int64":
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := validateScalar(rule.Schema, value); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var n int64
		var err error
		switch value := value.(type) {
		case string:
			n, err = strconv.ParseInt(value, 10, 64)
		case json.Number:
			n, err = parseInteger(value.String())
		default:
			err = fmt.Errorf("expected an integer or a decimal integer string")
		}
		if err != nil {
			return nil, fmt.Errorf("%s: cannot convert to int64: %w", path, err)
		}
		return []byte(strconv.FormatInt(n, 10)), nil
	case "object":
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		// Match encoding/json's case-insensitive field lookup, but reject aliases
		// appearing together so remarshal ordering cannot change the chosen value.
		seen := map[string]bool{}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			name := key
			field, ok := rule.Properties[name]
			if !ok {
				for candidate, candidateField := range rule.Properties {
					if strings.EqualFold(key, candidate) {
						name, field, ok = candidate, candidateField, true
						break
					}
				}
			}
			if !ok {
				continue
			}
			if seen[name] {
				return nil, fmt.Errorf("%s.%s: multiple spellings of the same field", path, name)
			}
			seen[name] = true
			value, err := normalize(object[key], field.Rule, path+"."+name, !field.Required)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		for name, field := range rule.Properties {
			if field.Required && !seen[name] {
				return nil, fmt.Errorf("%s.%s: required field is missing", path, name)
			}
		}
		return json.Marshal(object)
	case "array":
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for i, item := range items {
			value, err := normalize(item, rule.Items, fmt.Sprintf("%s[%d]", path, i), false)
			if err != nil {
				return nil, err
			}
			items[i] = value
		}
		return json.Marshal(items)
	default:
		return nil, fmt.Errorf("%s: unsupported decoding rule", path)
	}
}

// parseInteger accepts JSON numbers whose exact value is an int64, including
// integral decimal/exponent notation. It never rounds through float64 or
// allocates memory proportional to an untrusted exponent.
func parseInteger(s string) (int64, error) {
	if !strings.ContainsAny(s, ".eE") {
		return strconv.ParseInt(s, 10, 64)
	}
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	mantissa, exponent := s, "0"
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa, exponent = s[:i], s[i+1:]
	}
	fraction := 0
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		fraction = len(mantissa) - i - 1
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return 0, nil
	}
	e, err := strconv.ParseInt(exponent, 10, 64)
	if err != nil || e > int64(len(s))+19 || e < -int64(len(s)) {
		return 0, fmt.Errorf("number is not representable as int64")
	}
	scale := e - int64(fraction)
	trimmed := strings.TrimRight(digits, "0")
	scale += int64(len(digits) - len(trimmed))
	digits = trimmed
	if scale < 0 || scale > 19 || int64(len(digits))+scale > 19 {
		return 0, fmt.Errorf("number is not representable as int64")
	}
	return strconv.ParseInt(sign+digits+strings.Repeat("0", int(scale)), 10, 64)
}
