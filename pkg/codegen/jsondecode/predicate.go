package jsondecode

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// predicate specializes a schema for an already-decoded string or int64. Type
// branches and unconstrained schemas fold to constants during generation.
func (e *emitter) predicate(schema *openapi3.Schema, kind, value string) (string, error) {
	if kind == "integer" {
		if !schema.Type.Permits("integer") && !schema.Type.Permits("number") {
			return "false", nil
		}
	} else if !schema.Type.Permits(kind) {
		return "false", nil
	}
	var constraints []string
	for _, composite := range []struct {
		refs openapi3.SchemaRefs
		kind string
	}{{schema.AnyOf, "anyOf"}, {schema.OneOf, "oneOf"}, {schema.AllOf, "allOf"}} {
		if len(composite.refs) == 0 {
			continue
		}
		var branches []string
		for _, ref := range composite.refs {
			branch, err := e.predicate(ref.Value, kind, value)
			if err != nil {
				return "", err
			}
			branches = append(branches, branch)
		}
		switch composite.kind {
		case "anyOf":
			constraints = append(constraints, join("||", branches))
		case "allOf":
			constraints = append(constraints, join("&&", branches))
		case "oneOf":
			constraints = append(constraints, exactlyOne(branches))
		}
	}
	if schema.Not != nil {
		condition, err := e.predicate(schema.Not.Value, kind, value)
		if err != nil {
			return "", err
		}
		constraints = append(constraints, negate(condition))
	}
	if len(schema.Enum) > 0 {
		var values []string
		for _, allowed := range schema.Enum {
			if kind == "string" {
				if s, ok := allowed.(string); ok {
					values = append(values, value+" == "+strconv.Quote(s))
				}
			} else if n, ok := allowed.(float64); ok && math.Trunc(n) == n {
				values = append(values, fmt.Sprintf("%s == %d", value, int64(n)))
			}
		}
		constraints = append(constraints, join("||", values))
	}
	if kind == "string" {
		if schema.MinLength > 0 {
			e.use("unicode/utf8")
			constraints = append(constraints, fmt.Sprintf("uint64(utf8.RuneCountInString(%s)) >= %d", value, schema.MinLength))
		}
		if schema.MaxLength != nil {
			e.use("unicode/utf8")
			constraints = append(constraints, fmt.Sprintf("uint64(utf8.RuneCountInString(%s)) <= %d", value, *schema.MaxLength))
		}
		if schema.Pattern != "" {
			constraints = append(constraints, e.pattern(schema.Pattern, value))
		}
		// These are the string formats enabled by kin-openapi by default. Emit
		// their fixed checks instead of consulting its runtime validator registry.
		formats := map[string]string{
			"byte":      openapi3.FormatOfStringByte,
			"date":      openapi3.FormatOfStringDate,
			"date-time": openapi3.FormatOfStringDateTime,
		}
		if pattern := formats[schema.Format]; pattern != "" {
			constraints = append(constraints, e.pattern(pattern, value))
		}
	} else {
		if schema.Type.Is("integer") && schema.Format == "int32" {
			constraints = append(constraints, fmt.Sprintf("%s >= -2147483648 && %s <= 2147483647", value, value))
		}
		if schema.Min != nil {
			bound := int64(math.Ceil(*schema.Min))
			if schema.ExclusiveMin {
				bound = int64(math.Floor(*schema.Min)) + 1
			}
			constraints = append(constraints, fmt.Sprintf("%s >= %d", value, bound))
		}
		if schema.Max != nil {
			bound := int64(math.Floor(*schema.Max))
			if schema.ExclusiveMax {
				bound = int64(math.Ceil(*schema.Max)) - 1
			}
			constraints = append(constraints, fmt.Sprintf("%s <= %d", value, bound))
		}
		if schema.MultipleOf != nil {
			// For integer n and a reduced rational p/q, n/(p/q) is an
			// integer exactly when n is a multiple of p. Compute p now.
			r, ok := new(big.Rat).SetString(strconv.FormatFloat(*schema.MultipleOf, 'g', -1, 64))
			if !ok || r.Sign() <= 0 {
				return "", fmt.Errorf("multipleOf must be positive")
			}
			p := r.Num()
			switch {
			case p.IsInt64():
				if p.Int64() != 1 {
					constraints = append(constraints, fmt.Sprintf("%s %% %s == 0", value, p.String()))
				}
			case p.Cmp(new(big.Int).Lsh(big.NewInt(1), 63)) == 0:
				constraints = append(constraints, fmt.Sprintf("%s == 0 || %s == -9223372036854775808", value, value))
			default:
				constraints = append(constraints, value+" == 0")
			}
		}
	}
	return join("&&", constraints), nil
}

func (e *emitter) pattern(pattern, value string) string {
	name := e.patterns[pattern]
	if name == "" {
		e.use("regexp")
		name = e.symbol() + "Pattern"
		e.patterns[pattern] = name
	}
	return name + ".MatchString(" + value + ")"
}

func join(operator string, expressions []string) string {
	identity, absorbing := "true", "false"
	if operator == "||" {
		identity, absorbing = absorbing, identity
	}
	var filtered []string
	for _, expression := range expressions {
		if expression == absorbing {
			return absorbing
		}
		if expression != identity {
			filtered = append(filtered, "("+expression+")")
		}
	}
	if len(filtered) == 0 {
		return identity
	}
	return strings.Join(filtered, " "+operator+" ")
}

func negate(expression string) string {
	switch expression {
	case "true":
		return "false"
	case "false":
		return "true"
	default:
		return "!(" + expression + ")"
	}
}

func exactlyOne(expressions []string) string {
	known := 0
	var dynamic []string
	for _, expression := range expressions {
		switch expression {
		case "true":
			known++
		case "false":
		default:
			dynamic = append(dynamic, expression)
		}
	}
	if known > 1 {
		return "false"
	}
	if known == 1 {
		return negate(join("||", dynamic))
	}
	if len(dynamic) <= 1 {
		return join("||", dynamic)
	}
	var body strings.Builder
	body.WriteString("func() bool { matches := 0\n")
	for _, expression := range dynamic {
		fmt.Fprintf(&body, "if %s { matches++ }\n", expression)
	}
	body.WriteString("return matches == 1 }()")
	return body.String()
}
