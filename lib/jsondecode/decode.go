// Package jsondecode provides small primitives used by generated JSON methods.
package jsondecode

import (
	"fmt"
)

// RawValue captures one annotated field without interpreting its schema.
// A repeated field (including a case-insensitive alias) is rejected.
// Use a fresh value for each enclosing UnmarshalJSON call.
type RawValue []byte

func (v *RawValue) UnmarshalJSON(data []byte) error {
	if *v != nil {
		return fmt.Errorf("duplicate annotated JSON field")
	}
	*v = append((*v)[:0], data...)
	return nil
}

// ParseInt64Number converts a syntactically valid JSON number to int64 exactly.
// Generated callers obtain valid tokens from encoding/json or json.Valid.
// Integral decimal/exponent notation is accepted without rounding through
// float64 or allocating memory proportional to an untrusted exponent.
func ParseInt64Number(s string) (int64, error) {
	return ParseIntNumber(s, 64)
}
