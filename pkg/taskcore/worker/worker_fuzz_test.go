package worker

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func FuzzParseRuntimeConfigNotificationPayload(f *testing.F) {
	f.Add(`{"op":"up_config","params":{"request_id":"r1","version":1}}`)
	f.Add(`{"op":"noop","params":{"request_id":"r1","version":1}}`)
	f.Add(`{"params":{"request_id":"r1","version":1}}`)
	f.Add(`{`)

	f.Fuzz(func(t *testing.T, payload string) {
		if len(payload) > 1024 {
			return
		}
		requestID, shouldRefresh, err := parseRuntimeConfigNotificationPayload(payload)
		if err != nil {
			return
		}
		if !shouldRefresh && requestID != "" {
			t.Fatalf("non-refresh path should not return requestID, got=%q", requestID)
		}
	})
}

func FuzzDecodeRuntimeConfigPayload(f *testing.F) {
	f.Add(`{"maxStrictPercentage":20,"labelWeights":{"default":1,"w1":5}}`)
	f.Add(`{"labelWeights":{"default":0}}`)
	f.Add(`{}`)
	f.Add(`{`)

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 2048 {
			return
		}
		_, _ = decodeRuntimeConfigPayload(json.RawMessage(raw))
	})
}

func FuzzBuildClaimWheel(f *testing.F) {
	f.Add("default:1,w1:2,w2:1")
	f.Add("")
	f.Add("w1:0,w2:-1,w3:2")

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1024 {
			return
		}
		weights := parseWeightsMap(raw, 64)
		wheel := buildClaimWheel(weights)

		expected := 0
		for _, weight := range weights {
			if weight > 0 {
				expected += int(weight)
			}
		}
		if len(wheel) != expected {
			t.Fatalf("wheel size mismatch: got=%d want=%d", len(wheel), expected)
		}
		for _, group := range wheel {
			if weights[group] <= 0 {
				t.Fatalf("wheel contains non-positive group %q with weight=%d", group, weights[group])
			}
		}
	})
}

func FuzzApplyRuntimeConfigLocked(f *testing.F) {
	f.Add(10, int64(5), int32(30), true, "default:1,w1:5,w2:1")
	f.Add(1, int64(1), int32(0), true, "")
	f.Add(0, int64(2), int32(200), true, "default:0")
	f.Add(8, int64(3), int32(-10), true, "w1:1")
	f.Add(8, int64(4), int32(0), false, "w1:1")

	f.Fuzz(func(t *testing.T, concurrency int, version int64, percentage int32, withPercentage bool, raw string) {
		if len(raw) > 1024 {
			return
		}
		if concurrency < 0 {
			concurrency = -concurrency
		}
		concurrency = concurrency % 256

		payload := runtimeConfigPayload{
			LabelWeights: parseWeightsMap(raw, 64),
		}
		if withPercentage {
			payload.MaxStrictPercentage = &percentage
		}

		w := &Worker{concurrency: concurrency}
		w.applyRuntimeConfigLocked(version, payload)

		if w.runtimeConfigVersion != version {
			t.Fatalf("version mismatch: got=%d want=%d", w.runtimeConfigVersion, version)
		}
		if w.strictCap < 0 || w.strictCap > w.concurrency {
			t.Fatalf("strict cap out of range: cap=%d concurrency=%d", w.strictCap, w.concurrency)
		}
		if len(w.normalClaimWheel) == 0 {
			t.Fatal("normal claim wheel must not be empty")
		}

		hasDefault := false
		for _, group := range w.normalClaimWheel {
			if group == defaultWeightGroup {
				hasDefault = true
			}
		}
		if !hasDefault {
			t.Fatalf("normal claim wheel missing default group: %#v", w.normalClaimWheel)
		}

		sortedLabels := append([]string(nil), w.weightedLabels...)
		sort.Strings(sortedLabels)
		for i := range sortedLabels {
			if sortedLabels[i] != w.weightedLabels[i] {
				t.Fatalf("weighted labels not sorted: %#v", w.weightedLabels)
			}
			if sortedLabels[i] == defaultWeightGroup {
				t.Fatalf("default group leaked into weighted labels: %#v", w.weightedLabels)
			}
		}
	})
}

func FuzzStrictCapForPercentage(f *testing.F) {
	f.Add(0, int32(0))
	f.Add(10, int32(1))
	f.Add(10, int32(100))
	f.Add(10, int32(250))
	f.Add(-5, int32(50))

	f.Fuzz(func(t *testing.T, concurrency int, percentage int32) {
		if concurrency < -1024 || concurrency > 1024 {
			return
		}
		cap := strictCapForPercentage(concurrency, percentage)

		if cap < 0 {
			t.Fatalf("strict cap must not be negative: %d", cap)
		}
		if concurrency > 0 && cap > concurrency {
			t.Fatalf("strict cap cannot exceed concurrency: cap=%d concurrency=%d", cap, concurrency)
		}
		if concurrency <= 0 || percentage <= 0 {
			if cap != 0 {
				t.Fatalf("non-positive concurrency/percentage must yield zero cap: got=%d", cap)
			}
		}
		if concurrency > 0 && percentage >= 100 && cap != concurrency {
			t.Fatalf("percentage >= 100 should saturate concurrency: cap=%d concurrency=%d", cap, concurrency)
		}
	})
}

func parseWeightsMap(raw string, maxPairs int) map[string]int32 {
	ret := map[string]int32{}
	if raw == "" {
		return ret
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxPairs {
		parts = parts[:maxPairs]
	}
	for _, part := range parts {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		weight, err := strconv.ParseInt(kv[1], 10, 32)
		if err != nil {
			ret[kv[0]] = 0
			continue
		}
		ret[kv[0]] = int32(weight)
	}
	return ret
}
