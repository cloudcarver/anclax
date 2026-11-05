package jsondecode

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/getkin/kin-openapi/openapi3"
)

// validateScalar uses kin-openapi for scalar constraints, but checks numeric
// constraints exactly: VisitJSON otherwise converts even json.Number to float64.
func validateScalar(schema *openapi3.Schema, value any) error {
	if len(schema.AnyOf) > 0 {
		matched := false
		for _, branch := range schema.AnyOf {
			if err := validateScalar(branch.Value, value); err == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("value does not match anyOf")
		}
	}
	if len(schema.OneOf) > 0 {
		matched := 0
		for _, branch := range schema.OneOf {
			if err := validateScalar(branch.Value, value); err == nil {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("value must match exactly one oneOf branch")
		}
	}
	for _, branch := range schema.AllOf {
		if err := validateScalar(branch.Value, value); err != nil {
			return err
		}
	}
	if schema.Not != nil && validateScalar(schema.Not.Value, value) == nil {
		return fmt.Errorf("value matches the excluded schema")
	}
	base := *schema
	base.AnyOf, base.OneOf, base.AllOf, base.Not = nil, nil, nil, nil
	number, numeric := value.(json.Number)
	if !numeric {
		return base.VisitJSON(value)
	}
	if !schema.Type.Permits("integer") && !schema.Type.Permits("number") {
		return fmt.Errorf("expected %v", schema.Type.Slice())
	}
	n, err := parseInteger(number.String())
	if err != nil {
		return err
	}
	if schema.Type.Is("integer") {
		if format, ok := openapi3.SchemaIntegerFormats[schema.Format]; ok {
			if err := format.Validate(n); err != nil {
				return err
			}
		}
	}
	exact := new(big.Rat).SetInt64(n)
	for _, bound := range []struct {
		name      string
		value     *float64
		minimum   bool
		exclusive bool
	}{
		{"minimum", schema.Min, true, schema.ExclusiveMin},
		{"maximum", schema.Max, false, schema.ExclusiveMax},
	} {
		if bound.value == nil {
			continue
		}
		cmp := exact.Cmp(decimalRat(*bound.value))
		if bound.minimum && cmp < 0 || !bound.minimum && cmp > 0 || bound.exclusive && cmp == 0 {
			return fmt.Errorf("number violates %s", bound.name)
		}
	}
	if schema.MultipleOf != nil && !new(big.Rat).Quo(exact, decimalRat(*schema.MultipleOf)).IsInt() {
		return fmt.Errorf("number violates multipleOf")
	}
	if len(schema.Enum) > 0 {
		for _, allowed := range schema.Enum {
			if v, ok := allowed.(float64); ok && exact.Cmp(decimalRat(v)) == 0 {
				return nil
			}
		}
		return fmt.Errorf("number is not one of the enum values")
	}
	return nil
}

func decimalRat(value float64) *big.Rat {
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	return r
}
