package jsondecode

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

func TestIntegralJSONNotation(t *testing.T) {
	for _, n := range []int64{math.MinInt64, math.MaxInt64, -123, 0, 123, 9007199254740993} {
		inputs := []string{fmt.Sprint(n), fmt.Sprintf("%d.000", n), fmt.Sprintf("%de+0", n)}
		if n != 0 {
			inputs = append(inputs, fmt.Sprintf("%d00e-2", n))
		}
		for _, input := range inputs {
			if got, err := ParseInt64Number(input); err != nil || got != n {
				t.Errorf("%s: got %d, %v; want %d", input, got, err, n)
			}
		}
	}
	for _, input := range []string{"0e99999999999999999999999", "-0e-99999999999999999999999"} {
		if got, err := ParseInt64Number(input); err != nil || got != 0 {
			t.Errorf("%s: got %d, %v; want zero", input, got, err)
		}
	}
}

func TestRejectFractionalOrOverflowingNumbers(t *testing.T) {
	for _, input := range []string{
		"9223372036854775808", "-9223372036854775809", "9223372036854775808.0",
		"1.5", "9007199254740993.1", "1e-99", "1e99999999999999999999999", "1e-99999999999999999999999",
	} {
		if got, err := ParseInt64Number(input); err == nil {
			t.Errorf("accepted %s as %d", input, got)
		}
	}
}

func TestRawValueRejectsDuplicates(t *testing.T) {
	for _, input := range []string{`{"key":1,"key":2}`, `{"key":1,"KEY":2}`, `{"key":null,"key":2}`} {
		var value struct {
			Key RawValue `json:"key"`
		}
		if err := json.Unmarshal([]byte(input), &value); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}
