package jsondecode

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
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

func TestUnquoteStringMatchesJSON(t *testing.T) {
	for _, input := range []string{
		`""`, `"123"`, `"hello world"`, `"true"`, "\"\x7f\"",
		`"你好"`, `"\u4f60\u597d"`, `"\ud83d\ude00"`, `"\ud800"`, `"\udfff"`,
		`"\"\\\/\b\f\n\r\t"`, "\"\xff\xc0\xaf\"",
		`"` + strings.Repeat("a", 126) + `"`, `"` + strings.Repeat("a", 127) + `"`,
		`"` + strings.Repeat("a", 4096) + `你"`,
		` "123" `, `null`, `123`, `true`, `[]`, `{}`, ``, `"`, `"unterminated`,
		`"a"b"`, `"\x41"`, `"\uZZZZ"`, "\"a\nb\"", `"x" "y"`,
	} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			checkUnquoteString(t, []byte(input))
		})
	}
}

func FuzzUnquoteString(f *testing.F) {
	for _, input := range []string{`""`, `"123"`, `"你好"`, `"\ud800"`, `"\"\\\n"`, "\"\xff\"", `null`, `"x" "y"`} {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkUnquoteString(t, data) })
}

func checkUnquoteString(t *testing.T, data []byte) {
	t.Helper()
	var want string
	wantErr := json.Unmarshal(data, &want)
	got, err := UnquoteString(data)
	if got != want || (err == nil) != (wantErr == nil) {
		t.Fatalf("%q: got %q, %v; want %q, %v", data, got, err, want, wantErr)
	}
	if err == nil {
		// The decoder may reuse its input buffer after returning.
		for i := range data {
			data[i] = 'x'
		}
		if got != want {
			t.Fatalf("result borrowed the input buffer: got %q, want %q", got, want)
		}
	}
}
