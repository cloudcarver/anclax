package jsondecode

import (
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"testing"
)

func TestUnsignedAndSignedWidths(t *testing.T) {
	for _, input := range []string{"18446744073709551615", "18446744073709551615.0", "184467440737095516150e-1"} {
		if got, err := ParseUintNumber(input, 64); err != nil || got != math.MaxUint64 {
			t.Errorf("%s: %d, %v", input, got, err)
		}
	}
	for _, input := range []string{"-0", "-0.0", "-1", "18446744073709551616", "18446744073709551615.1"} {
		if _, err := ParseUintNumber(input, 64); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	for _, bits := range []int{8, 16, 32} {
		maxSigned := int64(1)<<(bits-1) - 1
		minSigned := -maxSigned - 1
		maxUnsigned := uint64(1)<<bits - 1
		for _, n := range []int64{minSigned, maxSigned} {
			if got, err := ParseIntNumber(fmt.Sprintf("%d.0", n), bits); err != nil || got != n {
				t.Errorf("int%d %d: %d, %v", bits, n, got, err)
			}
		}
		for _, n := range []int64{minSigned - 1, maxSigned + 1} {
			if _, err := ParseIntNumber(fmt.Sprint(n), bits); err == nil {
				t.Errorf("int%d accepted %d", bits, n)
			}
		}
		if got, err := ParseUintNumber(fmt.Sprintf("%de0", maxUnsigned), bits); err != nil || got != maxUnsigned {
			t.Errorf("uint%d: %d, %v", bits, got, err)
		}
		if _, err := ParseUintNumber(fmt.Sprint(maxUnsigned+1), bits); err == nil {
			t.Errorf("uint%d overflow accepted", bits)
		}
	}
}

func TestDecimalFloatStrings(t *testing.T) {
	for input, want := range map[string]float64{"+01.25": 1.25, ".5": .5, "1.": 1, "-2.5e2": -250, "1e-99999": 0} {
		if got, err := ParseFloatString(input, 64); err != nil || got != want {
			t.Errorf("%s: %v, %v", input, got, err)
		}
	}
	for _, input := range []string{"", " 1 ", "NaN", "Inf", "+Inf", "Infinity", "0x1p2", "1_000", "1e99999"} {
		if _, err := ParseFloatString(input, 64); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	if _, err := ParseFloatString("3.5e38", 32); err == nil {
		t.Fatal("float32 overflow accepted")
	}
}

func TestExactNumberAgainstRationalArithmetic(t *testing.T) {
	random := rand.New(rand.NewSource(14))
	for i := 0; i < 1000; i++ {
		left := fmt.Sprintf("%de%d", random.Int63n(2000001)-1000000, random.Intn(201)-100)
		right := fmt.Sprintf("%de%d", random.Int63n(1000000)+1, random.Intn(201)-100)
		a, b := NumberFromJSON(left), NumberFromJSON(right)
		ra, _ := new(big.Rat).SetString(left)
		rb, _ := new(big.Rat).SetString(right)
		if got := a.Cmp(b); got != ra.Cmp(rb) {
			t.Fatalf("%s compared to %s: got %d", left, right, got)
		}
		if a.IsInteger() != ra.IsInt() {
			t.Fatalf("incorrect integrality: %s", left)
		}
		want := new(big.Rat).Quo(ra, rb).IsInt()
		if a.MultipleOf(b) != want {
			t.Fatalf("%s multiple of %s: want %t", left, right, want)
		}
	}
}

func TestHugeExponentComparisons(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		cmp         int
		multiple    bool
	}{
		{"1e99999999999999999999999", "10e99999999999999999999998", 0, true},
		{"1e99999999999999999999999", "3", 1, false},
		{"1e99999999999999999999999", "0.5", 1, true},
		{"1e-99999999999999999999999", "0.1", -1, false},
		{"-0e99999999999999999999999", "0", 0, false},
		{"0", "3", -1, true},
		{"0.30", "0.1", 1, true},
		{"0.30000000000000001", "0.1", 1, false},
		{"9223372036854775807", "9223372036854775808", -1, false},
	} {
		a, b := NumberFromJSON(tc.left), NumberFromJSON(tc.right)
		if a.Cmp(b) != tc.cmp || a.MultipleOf(b) != tc.multiple {
			t.Errorf("%s versus %s: cmp=%d multiple=%t", tc.left, tc.right, a.Cmp(b), a.MultipleOf(b))
		}
	}
}
