// Package jsondecode provides small primitives used by generated JSON methods.
package jsondecode

import (
	"fmt"
	"strconv"
	"strings"
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
