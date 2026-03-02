package asynctask

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudcarver/anclax/pkg/taskcore/pgnotify"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
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
		params := &taskgen.UpdateWorkerRuntimeConfigParameters{
			Labels:  labels,
			Weights: weights,
		}
		if withDefault {
			params.DefaultWeight = &defaultWeight
		}

		labelWeights, err := buildLabelWeights(params)
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

func FuzzParseRuntimeConfigDurations(f *testing.F) {
	f.Add("1s", "2s", true, true)
	f.Add("", "", false, false)
	f.Add("x", "2s", true, true)
	f.Add("0s", "2s", true, true)

	f.Fuzz(func(t *testing.T, notify, listen string, withNotify, withListen bool) {
		if len(notify) > 64 || len(listen) > 64 {
			return
		}
		params := &taskgen.UpdateWorkerRuntimeConfigParameters{}
		if withNotify {
			params.NotifyInterval = &notify
		}
		if withListen {
			params.ListenTimeout = &listen
		}

		notifyDur, listenDur, err := parseRuntimeConfigDurations(params)
		if err != nil {
			return
		}
		if notifyDur <= 0 || listenDur <= 0 {
			t.Fatalf("durations must be positive: %v %v", notifyDur, listenDur)
		}
	})
}

func FuzzIsRuntimeConfigAckForRequest(f *testing.F) {
	f.Add(`{"op":"ack","params":{"request_id":"r1"}}`, "r1")
	f.Add(`{"op":"up_config","params":{"request_id":"r1"}}`, "r1")
	f.Add(`{`, "r1")
	f.Add(`{"params":{"request_id":"r2"}}`, "r1")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, payload string, requestID string) {
		if len(payload) > 1024 || len(requestID) > 128 {
			return
		}
		got := isRuntimeConfigAckForRequest(payload, requestID)
		if !got {
			return
		}
		if requestID == "" {
			t.Fatal("empty requestID should not match")
		}
		var ack pgnotify.RuntimeConfigAckNotification
		if err := json.Unmarshal([]byte(payload), &ack); err != nil {
			t.Fatalf("matched invalid payload: %v", err)
		}
		if !pgnotify.MatchesOp(ack.Op, pgnotify.OpAck) {
			t.Fatalf("matched unexpected op: %q", ack.Op)
		}
		if ack.Params.RequestID != requestID {
			t.Fatalf("matched wrong requestID: got=%q want=%q", ack.Params.RequestID, requestID)
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
