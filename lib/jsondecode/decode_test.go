package jsondecode

import (
	"fmt"
	"math"
	"sync"
	"testing"
)

func TestValidateOriginalScalarConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, schema string
		valid        map[string]int64
		invalid      []string
	}{
		{
			name:    "branch constraints",
			schema:  `{"anyOf":[{"type":"integer","minimum":10,"maximum":20,"exclusiveMinimum":true,"exclusiveMaximum":true},{"type":"string","pattern":"^0[0-9]+$","minLength":3,"maxLength":5}]}`,
			valid:   map[string]int64{`12`: 12, `"012"`: 12, `"010"`: 10},
			invalid: []string{`10`, `20`, `"12"`, `"01"`, `"000001"`},
		},
		{
			name:    "enum preserves original type",
			schema:  `{"anyOf":[{"type":"integer"},{"type":"string"}],"enum":[12,"0012"]}`,
			valid:   map[string]int64{`12`: 12, `12.0`: 12, `"0012"`: 12},
			invalid: []string{`"12"`, `13`, `"0013"`},
		},
		{
			name:    "large integer multipleOf",
			schema:  `{"anyOf":[{"type":"integer","multipleOf":2},{"type":"string","enum":["4"]}]}`,
			valid:   map[string]int64{`9007199254740994`: 9007199254740994, `"4"`: 4},
			invalid: []string{`9007199254740993`, `9223372036854775807`},
		},
		{
			name:    "fractional bounds and multiples",
			schema:  `{"anyOf":[{"type":"integer","minimum":1.5,"maximum":5.5,"multipleOf":1.5}]}`,
			valid:   map[string]int64{`3`: 3, `3e0`: 3},
			invalid: []string{`1`, `2`, `4`, `6`},
		},
		{
			name:    "composition",
			schema:  `{"anyOf":[{"type":"integer"},{"type":"string"}],"allOf":[{"minimum":10}],"not":{"enum":[12,"12"]},"oneOf":[{"type":"integer"},{"type":"string","pattern":"^[0-9]+$"}]}`,
			valid:   map[string]int64{`13`: 13, `"1"`: 1},
			invalid: []string{`1`, `12`, `"12"`, `"+13"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoder, err := Compile(`{"kind":"int64","schema":` + tc.schema + `}`)
			if err != nil {
				t.Fatal(err)
			}
			for input, want := range tc.valid {
				var got int64
				if err := decoder.Unmarshal([]byte(input), &got); err != nil || got != want {
					t.Errorf("%s: got %d, %v; want %d", input, got, err, want)
				}
			}
			for _, input := range tc.invalid {
				var got int64
				if err := decoder.Unmarshal([]byte(input), &got); err == nil {
					t.Errorf("accepted %s as %d", input, got)
				}
			}
		})
	}
}

func TestDecoderConcurrentUse(t *testing.T) {
	decoder := MustCompile(`{"kind":"int64","schema":{"anyOf":[{"type":"integer"},{"type":"string","pattern":"^-?[0-9]+$"}]}}`)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			for _, input := range []string{`9223372036854775807`, `"9223372036854775807"`} {
				var n int64
				if err := decoder.Unmarshal([]byte(input), &n); err != nil || n != math.MaxInt64 {
					t.Errorf("concurrent decode: %d, %v", n, err)
				}
			}
		})
	}
	wg.Wait()
}

func TestMalformedInputDoesNotMutateDestination(t *testing.T) {
	decoder := MustCompile(`{"kind":"int64","schema":{"anyOf":[{"type":"integer"},{"type":"string"}]}}`)
	for _, input := range []string{`1 2`, `"1" true`, ``, `01`, `1.`, `+1`, `"abc"`, `null`, `9223372036854775808`} {
		n := int64(42)
		if err := decoder.Unmarshal([]byte(input), &n); err == nil || n != 42 {
			t.Errorf("%s: value=%d, err=%v", input, n, err)
		}
	}
}

func TestIntegralJSONNotation(t *testing.T) {
	decoder := MustCompile(`{"kind":"int64","schema":{"anyOf":[{"type":"integer"}]}}`)
	for _, n := range []int64{math.MinInt64, math.MaxInt64, -123, 0, 123, 9007199254740993} {
		inputs := []string{fmt.Sprint(n), fmt.Sprintf("%d.000", n), fmt.Sprintf("%de+0", n)}
		if n != 0 {
			inputs = append(inputs, fmt.Sprintf("%d00e-2", n))
		}
		for _, input := range inputs {
			var got int64
			if err := decoder.Unmarshal([]byte(input), &got); err != nil || got != n {
				t.Errorf("%s: got %d, %v; want %d", input, got, err, n)
			}
		}
	}
}
