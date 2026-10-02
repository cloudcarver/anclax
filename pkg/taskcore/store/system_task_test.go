package store

import (
	"context"
	"testing"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSystemSchedulingAndReadyStateCannotBeForgedByEnqueue(t *testing.T) {
	tags, serial, weight, reserved := []string{"quota"}, "serial", int32(2), "anclax:system:prefetch"
	for name, task := range map[string]apigen.Task{
		"system tags":         {Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "cancelTaskOnWorker"}, Attributes: apigen.TaskAttributes{Tags: &tags}},
		"system serial":       {Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "cancelTaskOnWorker"}, Attributes: apigen.TaskAttributes{SerialKey: &serial}},
		"system weight":       {Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "cancelTaskOnWorker"}, Attributes: apigen.TaskAttributes{Weight: &weight}},
		"ready":               {Status: apigen.TaskStatusReady, Spec: apigen.TaskSpec{Type: "business"}},
		"scheduler":           {Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "prefetchTasks"}},
		"reserved unique tag": {Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "business"}, UniqueTag: &reserved},
	} {
		t.Run(name, func(t *testing.T) {
			m := model.NewMockModelInterface(gomock.NewController(t))
			_, err := NewTaskStore(m).PushTask(context.Background(), &task)
			require.Error(t, err, "unsupported configuration must fail before database access")
		})
	}
}
