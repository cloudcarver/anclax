package jsondecode

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// predicate specializes the original input schema for a known JSON kind.
// Signed/unsigned inputs have already been parsed exactly. Other numeric
// targets validate the original decimal before any floating-point rounding.
func (e *emitter) predicate(schema *openapi3.Schema, kind, value string) (string, error) {
	numeric := kind == "signed" || kind == "unsigned" || kind == "number"
	var constraints []string
	if numeric {
		if !schema.Type.Permits("integer") && !schema.Type.Permits("number") {
			return "false", nil
		}
		if kind == "number" && !schema.Type.Permits("number") {
			constraints = append(constraints, value+".IsInteger()")
		}
	} else if !schema.Type.Permits(kind) {
		return "false", nil
	}
	for _, composite := range []struct {
		refs openapi3.SchemaRefs
		kind string
	}{
		{schema.AnyOf, "anyOf"}, {schema.OneOf, "oneOf"}, {schema.AllOf, "allOf"},
	} {
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
			switch v := allowed.(type) {
			case string:
				if kind == "string" {
					values = append(values, value+" == "+strconv.Quote(v))
				}
			case bool:
				if kind == "boolean" {
					values = append(values, fmt.Sprintf("%s == %t", value, v))
				}
			case float64:
				if kind == "number" {
					values = append(values, value+".Cmp("+e.number(v)+") == 0")
				} else if (kind == "signed" || kind == "unsigned") && math.Trunc(v) == v && (kind != "unsigned" || v >= 0) {
					values = append(values, fmt.Sprintf("%s == %d", value, int64(v)))
				}
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
		formats := map[string]string{"byte": openapi3.FormatOfStringByte, "date": openapi3.FormatOfStringDate, "date-time": openapi3.FormatOfStringDateTime}
		if pattern := formats[schema.Format]; pattern != "" {
			constraints = append(constraints, e.pattern(pattern, value))
		}
	} else if numeric {
		if schema.Type.Is("integer") {
			switch schema.Format {
			case "int32":
				constraints = append(constraints, e.numericBound(kind, value, -2147483648, true, false), e.numericBound(kind, value, 2147483647, false, false))
			case "int64":
				if kind == "unsigned" {
					constraints = append(constraints, value+" <= 9223372036854775807")
				}
				if kind == "number" {
					constraints = append(constraints, value+".Cmp("+e.numberLiteral("-9223372036854775808")+") >= 0", value+".Cmp("+e.numberLiteral("9223372036854775807")+") <= 0")
				}
			}
		}
		if schema.Min != nil {
			constraints = append(constraints, e.numericBound(kind, value, *schema.Min, true, schema.ExclusiveMin))
		}
		if schema.Max != nil {
			constraints = append(constraints, e.numericBound(kind, value, *schema.Max, false, schema.ExclusiveMax))
		}
		if schema.MultipleOf != nil {
			if kind == "number" {
				constraints = append(constraints, value+".MultipleOf("+e.number(*schema.MultipleOf)+")")
			} else {
				r, ok := new(big.Rat).SetString(strconv.FormatFloat(*schema.MultipleOf, 'g', -1, 64))
				if !ok || r.Sign() <= 0 {
					return "", fmt.Errorf("multipleOf must be positive")
				}
				p := r.Num()
				switch {
				case p.IsInt64() || kind == "unsigned" && p.IsUint64():
					if p.Cmp(big.NewInt(1)) != 0 {
						constraints = append(constraints, fmt.Sprintf("%s %% %s == 0", value, p.String()))
					}
				case kind == "signed" && p.Cmp(new(big.Int).Lsh(big.NewInt(1), 63)) == 0:
					constraints = append(constraints, fmt.Sprintf("%s == 0 || %s == -9223372036854775808", value, value))
				default:
					constraints = append(constraints, value+" == 0")
				}
			}
		}
	}
	return join("&&", constraints), nil
}

func (e *emitter) numericBound(kind, value string, bound float64, minimum, exclusive bool) string {
	if kind == "number" {
		op := ">="
		if minimum && exclusive {
			op = ">"
		}
		if !minimum {
			op = "<="
			if exclusive {
				op = "<"
			}
		}
		return value + ".Cmp(" + e.number(bound) + ") " + op + " 0"
	}
	var integer int64
	op := ">="
	if minimum {
		integer = int64(math.Ceil(bound))
		if exclusive {
			integer = int64(math.Floor(bound)) + 1
		}
	} else {
		op = "<="
		integer = int64(math.Floor(bound))
		if exclusive {
			integer = int64(math.Ceil(bound)) - 1
		}
	}
	if kind == "unsigned" && integer < 0 {
		return strconv.FormatBool(minimum)
	}
	return fmt.Sprintf("%s %s %d", value, op, integer)
}

func (e *emitter) number(value float64) string {
	return e.numberLiteral(strconv.FormatFloat(value, 'g', -1, 64))
}
func (e *emitter) numberLiteral(value string) string {
	name := e.numbers[value]
	if name == "" {
		name = e.symbol() + "Number"
		e.numbers[value] = name
		e.use(runtimeImport)
	}
	return name
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
