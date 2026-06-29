package asynctask

import (
	"strconv"
	"strings"
	"testing"
)

func FuzzBuildLabelWeights(f *testing.F) {
	f.Add("w1,w2", "5,1", int32(1), true)
	f.Add("", "", int32(1), false)
	f.Add("w1,,w2", "1,2,3", int32(1), true)
	f.Add("w1", "0", int32(1), true)
	f.Add("w1,w2,w3", "1,2", int32(3), true)

	f.Fuzz(func(t *testing.T, rawLabels string, rawWeights string, defaultWeight int32, withDefault bool) {
		if len(rawLabels) > 512 || len(rawWeights) > 512 {
			return
		}
		labels := splitTokens(rawLabels, 32)
		weights := splitInt32Tokens(rawWeights, 32)
		var defaultWeightPtr *int32
		if withDefault {
			defaultWeightPtr = &defaultWeight
		}

		labelWeights, err := buildLabelWeights(defaultWeightPtr, labels, weights)
		if err != nil {
			return
		}
		if labelWeights["default"] < 1 {
			t.Fatalf("default weight must be positive, got %d", labelWeights["default"])
		}
		for label, weight := range labelWeights {
			if label == "" {
				t.Fatalf("empty label key in result: %#v", labelWeights)
			}
			if weight < 1 {
				t.Fatalf("non-positive weight in result: %d for %q", weight, label)
			}
		}
	})
}

func splitTokens(raw string, max int) []string {
	if raw == "" {
		return nil
	}
	tokens := strings.Split(raw, ",")
	if len(tokens) > max {
		tokens = tokens[:max]
	}
	return tokens
}

func splitInt32Tokens(raw string, max int) []int32 {
	if raw == "" {
		return nil
	}
	tokens := strings.Split(raw, ",")
	if len(tokens) > max {
		tokens = tokens[:max]
	}
	ret := make([]int32, 0, len(tokens))
	for _, token := range tokens {
		v, err := strconv.ParseInt(token, 10, 32)
		if err != nil {
			ret = append(ret, 0)
			continue
		}
		ret = append(ret, int32(v))
	}
	return ret
}
