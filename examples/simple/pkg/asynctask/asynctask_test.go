package asynctask

import (
	"context"
	"errors"
	"testing"

	"myexampleapp/pkg/model"
	"myexampleapp/pkg/zgen/taskgen"

	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"go.uber.org/mock/gomock"
)

func TestCounterTasksUsePayloadAmount(t *testing.T) {
	for _, taskType := range []string{taskgen.IncrementCounter, taskgen.AutoIncrementCounter} {
		t.Run(taskType, func(t *testing.T) {
			m := model.NewMockModelInterface(gomock.NewController(t))
			handler := taskgen.NewTaskHandler(NewExecutor(m))
			wantErr := errors.New("database unavailable")
			m.EXPECT().IncrementCounter(gomock.Any(), int32(7)).Return(wantErr)
			err := handler.HandleTask(context.Background(), worker.Task{
				Spec: apigen.TaskSpec{Type: taskType, Payload: []byte(`{"amount":7}`)},
			})
			if !errors.Is(err, wantErr) {
				t.Fatalf("task error = %v, want %v", err, wantErr)
			}
		})
	}
}
