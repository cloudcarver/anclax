// Package jsondecode provides small primitives used by generated JSON methods.
package jsondecode

import (
	"encoding/json"
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

// UnquoteString preserves encoding/json's string decoding, including its
// replacement of invalid UTF-8 and unpaired UTF-16 surrogates. Small unescaped
// ASCII tokens avoid another JSON decoder; the bounded scan keeps the fallback
// cheap for long strings. Returned strings own their bytes.
func UnquoteString(data []byte) (string, error) {
	if len(data) >= 2 && len(data) <= 128 && data[0] == '"' && data[len(data)-1] == '"' {
		plain := true
		for _, c := range data[1 : len(data)-1] {
			if c < 0x20 || c >= 0x80 || c == '\\' || c == '"' {
				plain = false
				break
			}
		}
		if plain {
			return string(data[1 : len(data)-1]), nil
		}
	}
	var text string
	err := json.Unmarshal(data, &text)
	return text, err
}

// ParseInt64Number converts a syntactically valid JSON number to int64 exactly.
// Generated callers obtain valid tokens from encoding/json or json.Valid.
// Integral decimal/exponent notation is accepted without rounding through
// float64 or allocating memory proportional to an untrusted exponent.
func ParseInt64Number(s string) (int64, error) {
	return ParseIntNumber(s, 64)
}
