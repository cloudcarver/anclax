package asynctask

import (
	"context"
	"testing"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWorkerControlTaskHandlerApplyRuntimeConfigToTargetWorker(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorker := worker.NewMockWorkerInterface(ctrl)
	handler := NewWorkerControlTaskHandler(mockWorker)

	requestID := "req-1"
	params := &taskgen.ApplyWorkerRuntimeConfigToWorkerParameters{
		RequestID: &requestID,
		WorkerID:  "w-1",
		Version:   10,
	}
	payload, err := params.Marshal()
	require.NoError(t, err)

	mockWorker.EXPECT().WorkerID().Return("w-1")
	mockWorker.EXPECT().NotifyRuntimeConfig(requestID)

	err = handler.HandleTask(context.Background(), worker.Task{Spec: apigen.TaskSpec{
		Type:    taskgen.ApplyWorkerRuntimeConfigToWorker,
		Payload: payload,
	}})
	require.NoError(t, err)
}

func TestWorkerControlTaskHandlerApplyRuntimeConfigToDifferentWorkerNoop(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorker := worker.NewMockWorkerInterface(ctrl)
	handler := NewWorkerControlTaskHandler(mockWorker)

	params := &taskgen.ApplyWorkerRuntimeConfigToWorkerParameters{
		WorkerID: "w-2",
		Version:  10,
	}
	payload, err := params.Marshal()
	require.NoError(t, err)

	mockWorker.EXPECT().WorkerID().Return("w-1")
	mockWorker.EXPECT().NotifyRuntimeConfig(gomock.Any()).Times(0)

	err = handler.HandleTask(context.Background(), worker.Task{Spec: apigen.TaskSpec{
		Type:    taskgen.ApplyWorkerRuntimeConfigToWorker,
		Payload: payload,
	}})
	require.NoError(t, err)
}

func TestWorkerControlTaskHandlerUnknownTaskType(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorker := worker.NewMockWorkerInterface(ctrl)
	handler := NewWorkerControlTaskHandler(mockWorker)

	err := handler.HandleTask(context.Background(), worker.Task{Spec: apigen.TaskSpec{Type: "unknown"}})
	require.ErrorIs(t, err, worker.ErrUnknownTaskType)
}

func TestWorkerControlTaskHandlerOnFailedAndRegister(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorker := worker.NewMockWorkerInterface(ctrl)
	handler := NewWorkerControlTaskHandler(mockWorker)

	err := handler.OnTaskFailed(context.Background(), nil, worker.NewTaskSpec(apigen.TaskSpec{Type: "x"}), 1)
	require.ErrorIs(t, err, worker.ErrUnknownTaskType)

	// no-op, should not panic
	handler.RegisterTaskHandler(nil)
}

func TestWorkerControlTaskHandlerEmptyTargetWorkerIDAppliesToLocal(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorker := worker.NewMockWorkerInterface(ctrl)
	handler := NewWorkerControlTaskHandler(mockWorker)

	params := &taskgen.CancelTaskOnWorkerParameters{WorkerID: "", TaskIDs: []int32{9}}
	payload, err := params.Marshal()
	require.NoError(t, err)

	mockWorker.EXPECT().WorkerID().Times(0)
	mockWorker.EXPECT().InterruptTasks([]int32{9}, taskcore.ErrTaskCancelled)

	err = handler.HandleTask(context.Background(), worker.Task{Spec: apigen.TaskSpec{
		Type:    taskgen.CancelTaskOnWorker,
		Payload: payload,
	}})
	require.NoError(t, err)
}

func TestWorkerControlTaskHandlerPayloadParseError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorker := worker.NewMockWorkerInterface(ctrl)
	handler := NewWorkerControlTaskHandler(mockWorker)

	err := handler.HandleTask(context.Background(), worker.Task{Spec: apigen.TaskSpec{
		Type:    taskgen.CancelTaskOnWorker,
		Payload: []byte("not-json"),
	}})
	require.Error(t, err)
}

func TestWorkerControlTaskHandlerCancelAndPause(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorker := worker.NewMockWorkerInterface(ctrl)
	handler := NewWorkerControlTaskHandler(mockWorker)

	taskIDs := []int32{11, 22}
	cancelParams := &taskgen.CancelTaskOnWorkerParameters{WorkerID: "w-1", TaskIDs: taskIDs}
	cancelPayload, err := cancelParams.Marshal()
	require.NoError(t, err)

	pauseParams := &taskgen.PauseTaskOnWorkerParameters{WorkerID: "w-1", TaskIDs: taskIDs}
	pausePayload, err := pauseParams.Marshal()
	require.NoError(t, err)

	mockWorker.EXPECT().WorkerID().Return("w-1")
	mockWorker.EXPECT().InterruptTasks(taskIDs, taskcore.ErrTaskCancelled)
	mockWorker.EXPECT().WorkerID().Return("w-1")
	mockWorker.EXPECT().InterruptTasks(taskIDs, taskcore.ErrTaskPaused)

	err = handler.HandleTask(context.Background(), worker.Task{Spec: apigen.TaskSpec{
		Type:    taskgen.CancelTaskOnWorker,
		Payload: cancelPayload,
	}})
	require.NoError(t, err)

	err = handler.HandleTask(context.Background(), worker.Task{Spec: apigen.TaskSpec{
		Type:    taskgen.PauseTaskOnWorker,
		Payload: pausePayload,
	}})
	require.NoError(t, err)
}
