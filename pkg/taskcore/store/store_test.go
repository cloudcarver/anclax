package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestUpdateCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx            = context.Background()
		taskID         = int32(1)
		cronExpression = "*/5 * * * * *"
		taskSpec       = apigen.TaskSpec{
			Payload: json.RawMessage(`{}`),
		}
		currentTime      = time.Date(2025, 3, 31, 12, 0, 0, 0, time.UTC)
		expectedNextTime = time.Date(2025, 3, 31, 12, 0, 5, 0, time.UTC)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: cronExpression,
			},
		},
		SerialKey: nil,
		SerialID:  nil,
	}, nil)

	mockModel.EXPECT().UpdateTask(ctx, utils.NewJSONValueMatcher(t, querier.UpdateTaskParams{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: cronExpression,
			},
			Priority: utils.Ptr(int32(0)),
			Weight:   utils.Ptr(int32(1)),
		},
		Spec:      taskSpec,
		StartedAt: &expectedNextTime,
		SerialKey: nil,
		SerialID:  nil,
		Priority:  0,
		Weight:    1,
	}))

	taskStore := &TaskStore{
		model: mockModel,
		now: func() time.Time {
			return currentTime
		},
	}
	err := taskStore.UpdateCronJob(ctx, taskID, cronExpression, json.RawMessage(`{}`))
	require.NoError(t, err)
}

func TestUpdateCronJobPreservesSerialAttributes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx            = context.Background()
		taskID         = int32(1)
		cronExpression = "*/10 * * * * *"
		serialKey      = "order-42"
		serialID       = int32(5)
		taskSpec       = apigen.TaskSpec{
			Payload: json.RawMessage(`{"hello":"world"}`),
		}
		currentTime      = time.Date(2025, 4, 1, 12, 0, 0, 0, time.UTC)
		expectedNextTime = time.Date(2025, 4, 1, 12, 0, 10, 0, time.UTC)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			SerialKey: &serialKey,
			SerialID:  &serialID,
		},
		SerialKey: &serialKey,
		SerialID:  &serialID,
	}, nil)

	mockModel.EXPECT().UpdateTask(ctx, utils.NewJSONValueMatcher(t, querier.UpdateTaskParams{
		ID: taskID,
		Attributes: apigen.TaskAttributes{
			Cronjob: &apigen.TaskCronjob{
				CronExpression: cronExpression,
			},
			SerialKey: &serialKey,
			SerialID:  &serialID,
			Priority:  utils.Ptr(int32(0)),
			Weight:    utils.Ptr(int32(1)),
		},
		Spec:      taskSpec,
		StartedAt: &expectedNextTime,
		SerialKey: &serialKey,
		SerialID:  &serialID,
		Priority:  0,
		Weight:    1,
	}))

	taskStore := &TaskStore{
		model: mockModel,
		now: func() time.Time {
			return currentTime
		},
	}
	err := taskStore.UpdateCronJob(ctx, taskID, cronExpression, json.RawMessage(`{"hello":"world"}`))
	require.NoError(t, err)
}

func TestPushTaskRejectsSerialIDWithoutKey(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	serialID := int32(3)

	mockModel := model.NewMockModelInterface(ctrl)
	store := &TaskStore{model: mockModel}

	_, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialID: &serialID},
		Spec:       apigen.TaskSpec{Type: "serial", Payload: json.RawMessage(`{}`)},
		Status:     apigen.Pending,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "serialID requires serialKey")
}

func TestPushTaskRejectsEmptySerialKey(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	serialKey := ""

	mockModel := model.NewMockModelInterface(ctrl)
	store := &TaskStore{model: mockModel}

	_, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKey},
		Spec:       apigen.TaskSpec{Type: "serial", Payload: json.RawMessage(`{}`)},
		Status:     apigen.Pending,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "serialKey cannot be empty")
}

func TestPushTaskSerialAttributes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	serialKey := "order-99"
	serialID := int32(7)
	spec := apigen.TaskSpec{Type: "serial", Payload: json.RawMessage(`{"id":7}`)}

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().CreateTask(ctx, utils.NewJSONValueMatcher(t, querier.CreateTaskParams{
		Attributes: apigen.TaskAttributes{
			SerialKey: &serialKey,
			SerialID:  &serialID,
			Priority:  utils.Ptr(int32(0)),
			Weight:    utils.Ptr(int32(1)),
		},
		Spec:      spec,
		Status:    string(apigen.Pending),
		StartedAt: nil,
		UniqueTag: nil,
		SerialKey: &serialKey,
		SerialID:  &serialID,
		Priority:  0,
		Weight:    1,
	})).Return(&querier.AnclaxTask{ID: 42}, nil)

	store := &TaskStore{model: mockModel}
	id, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{SerialKey: &serialKey, SerialID: &serialID},
		Spec:       spec,
		Status:     apigen.Pending,
	})
	require.NoError(t, err)
	require.Equal(t, int32(42), id)
}

func TestPushTaskParentTaskID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	parentTaskID := int32(41)
	spec := apigen.TaskSpec{Type: "child", Payload: json.RawMessage(`{"id":41}`)}

	task := &apigen.Task{
		Attributes: apigen.TaskAttributes{},
		Spec:       spec,
		Status:     apigen.Pending,
	}
	require.NoError(t, WithParentTaskID(parentTaskID)(task))

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().CreateTask(ctx, utils.NewJSONValueMatcher(t, querier.CreateTaskParams{
		Attributes: apigen.TaskAttributes{
			Priority: utils.Ptr(int32(0)),
			Weight:   utils.Ptr(int32(1)),
		},
		Spec:         spec,
		Status:       string(apigen.Pending),
		StartedAt:    nil,
		UniqueTag:    nil,
		ParentTaskID: &parentTaskID,
		SerialKey:    nil,
		SerialID:     nil,
		Priority:     0,
		Weight:       1,
	})).Return(&querier.AnclaxTask{ID: 77}, nil)

	store := &TaskStore{model: mockModel}
	id, err := store.PushTask(ctx, task)
	require.NoError(t, err)
	require.Equal(t, int32(77), id)
}

func TestPushTaskUniqueTagReturnsExisting(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	uniqueTag := "unique-task"

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByUniqueTag(ctx, &uniqueTag).Return(&querier.AnclaxTask{ID: 99}, nil)

	store := &TaskStore{model: mockModel}
	id, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{},
		Spec:       apigen.TaskSpec{Type: "unique", Payload: json.RawMessage(`{"id":1}`)},
		Status:     apigen.Pending,
		UniqueTag:  &uniqueTag,
	})
	require.NoError(t, err)
	require.Equal(t, int32(99), id)
}

func TestPushTaskUniqueTagLookupFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	uniqueTag := "unique-task"

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByUniqueTag(ctx, &uniqueTag).Return(nil, errors.New("boom"))

	store := &TaskStore{model: mockModel}
	_, err := store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{},
		Spec:       apigen.TaskSpec{Type: "unique", Payload: json.RawMessage(`{"id":1}`)},
		Status:     apigen.Pending,
		UniqueTag:  &uniqueTag,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to check task by unique tag before push")
}

func TestGetTaskByUniqueTag(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	uniqueTag := "unique-tag"

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByUniqueTag(ctx, &uniqueTag).Return(&querier.AnclaxTask{
		ID:         10,
		Attributes: apigen.TaskAttributes{},
		Spec:       apigen.TaskSpec{Type: "demo"},
		Status:     string(apigen.Pending),
	}, nil)

	store := &TaskStore{model: mockModel}
	task, err := store.GetTaskByUniqueTag(ctx, uniqueTag)
	require.NoError(t, err)
	require.Equal(t, int32(10), task.ID)
	require.Equal(t, apigen.Pending, task.Status)
}

func TestGetTaskByUniqueTagNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	uniqueTag := "unique-tag"

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByUniqueTag(ctx, &uniqueTag).Return(nil, pgx.ErrNoRows)

	store := &TaskStore{model: mockModel}
	_, err := store.GetTaskByUniqueTag(ctx, uniqueTag)
	require.ErrorIs(t, err, ErrTaskNotFound)
}

func TestPauseCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx    = context.Background()
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Paused),
	}).Return(nil)

	taskStore := &TaskStore{
		model: mockModel,
	}

	err := taskStore.PauseTask(ctx, taskID)
	require.NoError(t, err)
}

func TestCancelTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx    = context.Background()
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Cancelled),
	}).Return(nil)

	taskStore := &TaskStore{
		model: mockModel,
	}
	err := taskStore.CancelTask(ctx, taskID)
	require.NoError(t, err)
}

func TestResumeCronJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var (
		ctx    = context.Background()
		taskID = int32(1)
	)

	mockModel := model.NewMockModelInterface(ctrl)

	mockModel.EXPECT().UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Pending),
	}).Return(nil)

	taskStore := &TaskStore{
		model: mockModel,
	}
	err := taskStore.ResumeTask(ctx, taskID)
	require.NoError(t, err)
}

func TestGetTaskByID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(1)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(&querier.AnclaxTask{
		ID:         taskID,
		Attributes: apigen.TaskAttributes{},
		Spec:       apigen.TaskSpec{},
		Status:     string(apigen.Pending),
		Attempts:   2,
		SerialKey:  nil,
		SerialID:   nil,
	}, nil)

	store := &TaskStore{model: mockModel}
	task, err := store.GetTaskByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, taskID, task.ID)
	require.Equal(t, apigen.Pending, task.Status)
}

func TestGetTaskByIDNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(1)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetTaskByID(ctx, taskID).Return(nil, pgx.ErrNoRows)

	store := &TaskStore{model: mockModel}
	_, err := store.GetTaskByID(ctx, taskID)
	require.ErrorIs(t, err, ErrTaskNotFound)
}

func TestGetLastTaskErrorEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	createdAt := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	taskID := int32(1)

	spec := apigen.EventSpec{
		Type: apigen.TaskError,
		TaskError: &apigen.EventTaskError{
			TaskID: taskID,
			Error:  "boom",
		},
	}

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLastTaskErrorEvent(ctx, taskID).Return(&querier.AnclaxEvent{
		ID:        10,
		Spec:      spec,
		CreatedAt: createdAt,
	}, nil)

	store := &TaskStore{model: mockModel}
	event, err := store.GetLastTaskErrorEvent(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, int32(10), event.ID)
	require.Equal(t, apigen.TaskError, event.Spec.Type)
	require.NotNil(t, event.Spec.TaskError)
	require.Equal(t, "boom", event.Spec.TaskError.Error)
}

func TestGetLastTaskErrorEventNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	taskID := int32(1)

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().GetLastTaskErrorEvent(ctx, taskID).Return(nil, pgx.ErrNoRows)

	store := &TaskStore{model: mockModel}
	_, err := store.GetLastTaskErrorEvent(ctx, taskID)
	require.ErrorIs(t, err, ErrTaskEventNotFound)
}

func TestUpdatePendingTaskPriorityByLabels(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	labels := []string{"billing"}

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().UpdatePendingTaskPriorityByLabels(ctx, querier.UpdatePendingTaskPriorityByLabelsParams{
		Priority:  7,
		HasLabels: true,
		Labels:    labels,
	}).Return(int64(3), nil)

	store := &TaskStore{model: mockModel}
	rows, err := store.UpdatePendingTaskPriorityByLabels(ctx, labels, 7)
	require.NoError(t, err)
	require.Equal(t, int64(3), rows)
}

func TestUpdatePendingTaskPriorityByLabelsRejectsNegative(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	store := &TaskStore{model: model.NewMockModelInterface(ctrl)}
	_, err := store.UpdatePendingTaskPriorityByLabels(context.Background(), []string{"billing"}, -1)
	require.Error(t, err)
}

func TestUpdatePendingTaskWeightByLabels(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	labels := []string{"billing"}

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().UpdatePendingTaskWeightByLabels(ctx, querier.UpdatePendingTaskWeightByLabelsParams{
		Weight:    5,
		HasLabels: true,
		Labels:    labels,
	}).Return(int64(2), nil)

	store := &TaskStore{model: mockModel}
	rows, err := store.UpdatePendingTaskWeightByLabels(ctx, labels, 5)
	require.NoError(t, err)
	require.Equal(t, int64(2), rows)
}

func TestUpdatePendingTaskWeightByLabelsRejectsNonPositive(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	store := &TaskStore{model: model.NewMockModelInterface(ctrl)}
	_, err := store.UpdatePendingTaskWeightByLabels(context.Background(), nil, 0)
	require.Error(t, err)
}

func TestUpdatePendingTaskPriorityByEmptyLabelsTargetsDefaultGroup(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().UpdatePendingTaskPriorityByLabels(ctx, querier.UpdatePendingTaskPriorityByLabelsParams{
		Priority:  2,
		HasLabels: false,
		Labels:    nil,
	}).Return(int64(1), nil)

	store := &TaskStore{model: mockModel}
	rows, err := store.UpdatePendingTaskPriorityByLabels(ctx, nil, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
}

func TestUpdatePendingTaskWeightByEmptyLabelsTargetsDefaultGroup(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().UpdatePendingTaskWeightByLabels(ctx, querier.UpdatePendingTaskWeightByLabelsParams{
		Weight:    3,
		HasLabels: false,
		Labels:    nil,
	}).Return(int64(4), nil)

	store := &TaskStore{model: mockModel}
	rows, err := store.UpdatePendingTaskWeightByLabels(ctx, nil, 3)
	require.NoError(t, err)
	require.Equal(t, int64(4), rows)
}
