package taskcore

import (
	"testing"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
)

func FuzzWithPriorityAndWithWeight(f *testing.F) {
	f.Add(int32(0), int32(1))
	f.Add(int32(5), int32(10))
	f.Add(int32(-1), int32(1))
	f.Add(int32(1), int32(0))

	f.Fuzz(func(t *testing.T, priority int32, weight int32) {
		task := &apigen.Task{Attributes: apigen.TaskAttributes{}}

		errPriority := WithPriority(priority)(task)
		if priority < 0 {
			if errPriority == nil {
				t.Fatalf("expected error for priority=%d", priority)
			}
			return
		}
		if errPriority != nil {
			t.Fatalf("unexpected priority error: %v", errPriority)
		}
		if task.Attributes.Priority == nil || *task.Attributes.Priority != priority {
			t.Fatalf("priority not applied: %#v", task.Attributes.Priority)
		}

		errWeight := WithWeight(weight)(task)
		if weight < 1 {
			if errWeight == nil {
				t.Fatalf("expected error for weight=%d", weight)
			}
			return
		}
		if errWeight != nil {
			t.Fatalf("unexpected weight error: %v", errWeight)
		}
		if task.Attributes.Weight == nil || *task.Attributes.Weight != weight {
			t.Fatalf("weight not applied: %#v", task.Attributes.Weight)
		}
	})
}
