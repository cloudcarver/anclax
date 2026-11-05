package jsondecode

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// ParseIntNumber parses a valid JSON number exactly at the requested signed
// width. Integral decimal/exponent notation is accepted; fractions never round.
func ParseIntNumber(s string, bits int) (int64, error) {
	if !strings.ContainsAny(s, ".eE") {
		return strconv.ParseInt(s, 10, bits)
	}
	digits, err := integerDigits(s)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(digits, 10, bits)
}

// ParseUintNumber is the unsigned counterpart. A negative sign is rejected,
// including -0. No signed intermediate limits the uint64 range.
func ParseUintNumber(s string, bits int) (uint64, error) {
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("negative sign is not allowed for unsigned integers")
	}
	if !strings.ContainsAny(s, ".eE") {
		return strconv.ParseUint(s, 10, bits)
	}
	digits, err := integerDigits(s)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(digits, 10, bits)
}

func integerDigits(s string) (string, error) {
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
		return "0", nil
	}
	e, err := strconv.ParseInt(exponent, 10, 64)
	if err != nil || e > int64(len(s))+20 || e < -int64(len(s)) {
		return "", fmt.Errorf("number is not representable as an integer")
	}
	scale := e - int64(fraction)
	trimmed := strings.TrimRight(digits, "0")
	scale += int64(len(digits) - len(trimmed))
	digits = trimmed
	if scale < 0 || scale > 20 || int64(len(digits))+scale > 20 {
		return "", fmt.Errorf("number is not representable as an integer")
	}
	return sign + digits + strings.Repeat("0", int(scale)), nil
}

// ParseFloatString accepts decimal notation with an optional sign, fractional
// part, and exponent. Hexadecimal, underscores, whitespace and non-finite values
// are rejected. Rounding (including underflow to zero) follows strconv.ParseFloat.
func ParseFloatString(s string, bits int) (float64, error) {
	for _, c := range s {
		if c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.' || c == 'e' || c == 'E' {
			continue
		}
		return 0, fmt.Errorf("expected a decimal floating-point string")
	}
	value, err := strconv.ParseFloat(s, bits)
	if err != nil {
		return 0, err
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("non-finite numbers are not allowed")
	}
	return value, nil
}

// Number is an immutable exact decimal used by generated numeric comparisons.
// It stores the exponent, never a power of ten, so very large exponents do not
// cause exponent-sized allocations. It contains no schema or validation rules.
type Number struct {
	negative bool
	digits   string
	scale    big.Int
}

// NumberFromJSON requires a syntactically valid JSON number, as provided by
// encoding/json or a generated method's json.Valid check.
func NumberFromJSON(s string) Number {
	var n Number
	if strings.HasPrefix(s, "-") {
		n.negative, s = true, s[1:]
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
		n.digits = "0"
		n.negative = false
		return n
	}
	n.digits = strings.TrimRight(digits, "0")
	n.scale.SetString(exponent, 10)
	n.scale.Add(&n.scale, big.NewInt(int64(len(digits)-len(n.digits)-fraction)))
	return n
}

func (n Number) IsInteger() bool { return n.scale.Sign() >= 0 }

func (n Number) Cmp(other Number) int {
	if n.digits == "0" && other.digits == "0" {
		return 0
	}
	if n.negative != other.negative {
		if n.negative {
			return -1
		}
		return 1
	}
	cmp := 0
	switch {
	case n.digits == "0":
		cmp = -1
	case other.digits == "0":
		cmp = 1
	default:
		var left, right big.Int
		left.Add(&n.scale, big.NewInt(int64(len(n.digits))))
		right.Add(&other.scale, big.NewInt(int64(len(other.digits))))
		cmp = left.Cmp(&right)
		if cmp == 0 {
			length := min(len(n.digits), len(other.digits))
			cmp = strings.Compare(n.digits[:length], other.digits[:length])
			if cmp == 0 {
				if len(n.digits) < len(other.digits) {
					cmp = -1
				}
				if len(n.digits) > len(other.digits) {
					cmp = 1
				}
			}
		}
	}
	if n.negative {
		return -cmp
	}
	return cmp
}

// MultipleOf compares exact decimal values, including fractional divisors,
// without constructing powers of ten for an input-controlled exponent.
func (n Number) MultipleOf(divisor Number) bool {
	if divisor.digits == "0" {
		return false
	}
	if n.digits == "0" {
		return true
	}
	var delta big.Int
	delta.Sub(&n.scale, &divisor.scale)
	// Canonical coefficients have no trailing zeros. A negative scale
	// difference cannot yield an integer quotient, even after cancellation.
	if delta.Sign() < 0 {
		return false
	}
	var numerator, denominator, gcd big.Int
	numerator.SetString(n.digits, 10)
	denominator.SetString(divisor.digits, 10)
	gcd.GCD(nil, nil, &numerator, &denominator)
	denominator.Quo(&denominator, &gcd)
	twos := denominator.TrailingZeroBits()
	denominator.Rsh(&denominator, twos)
	fives := int64(0)
	var quotient, remainder big.Int
	five := big.NewInt(5)
	for {
		quotient.QuoRem(&denominator, five, &remainder)
		if remainder.Sign() != 0 {
			break
		}
		denominator.Set(&quotient)
		fives++
	}
	return denominator.Cmp(big.NewInt(1)) == 0 && delta.Cmp(big.NewInt(max(int64(twos), fives))) >= 0
}
