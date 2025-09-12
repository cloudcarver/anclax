package taskcore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/types"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
)

var ErrTaskNotFound = errors.New("task not found")

type TaskStore struct {
	now func() time.Time

	model model.ModelInterface
}

func NewTaskStore(model model.ModelInterface) TaskStoreInterface {
	return &TaskStore{
		now:   time.Now,
		model: model,
	}
}

func (s *TaskStore) WithTx(tx pgx.Tx) TaskStoreInterface {
	return &TaskStore{
		now:   s.now,
		model: s.model.SpawnWithTx(tx),
	}
}

func (s *TaskStore) PushTask(ctx context.Context, task *apigen.Task) (int32, error) {
	if task.UniqueTag != nil {
		task, err := s.model.GetTaskByUniqueTag(ctx, task.UniqueTag)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, errors.Wrap(err, "failed to check task by unique tag before push")
		}
		return task.ID, nil
	}

	createdTask, err := s.model.CreateTask(ctx, querier.CreateTaskParams{
		Attributes: task.Attributes,
		Spec:       task.Spec,
		StartedAt:  task.StartedAt,
		Status:     string(task.Status),
		UniqueTag:  task.UniqueTag,
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to push task")
	}
	return createdTask.ID, nil
}

func (s *TaskStore) UpdateCronJob(ctx context.Context, taskID int32, cronExpression string, spec json.RawMessage) error {
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cron, err := parser.Parse(cronExpression)
	if err != nil {
		return errors.Wrapf(err, "failed to parse cron expression, format should be like second minute hour dayOfMonth month dayOfWeek")
	}
	nextTime := cron.Next(s.now())

	task, err := s.model.GetTaskByID(ctx, taskID)
	if err != nil {
		return errors.Wrapf(err, "failed to get task")
	}

	task.Attributes.Cronjob = &apigen.TaskCronjob{
		CronExpression: cronExpression,
	}

	task.Spec.Payload = spec

	if err := s.model.UpdateTask(ctx, querier.UpdateTaskParams{
		ID:         taskID,
		Attributes: task.Attributes,
		StartedAt:  &nextTime,
		Spec:       task.Spec,
	}); err != nil {
		return errors.Wrapf(err, "failed to update task")
	}
	return nil
}

func (s *TaskStore) PauseTask(ctx context.Context, taskID int32) error {
	if err := s.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Paused),
	}); err != nil {
		return errors.Wrapf(err, "failed to pause task")
	}
	return nil
}

func (s *TaskStore) ResumeTask(ctx context.Context, taskID int32) error {
	if err := s.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Pending),
	}); err != nil {
		return errors.Wrapf(err, "failed to resume task")
	}
	return nil
}

func (s *TaskStore) GetTaskByUniqueTag(ctx context.Context, uniqueTag string) (*apigen.Task, error) {
	task, err := s.model.GetTaskByUniqueTag(ctx, &uniqueTag)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, errors.Wrapf(err, "failed to get task by unique tag")
	}
	ret := types.TaskToAPI(task)
	return &ret, nil
}
