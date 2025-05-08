package task

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudcarver/anchor/pkg/apigen"
	"github.com/cloudcarver/anchor/pkg/model"
	"github.com/cloudcarver/anchor/pkg/model/querier"
	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
)

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

func (s *TaskStore) PushTask(ctx context.Context, task *apigen.Task) (int32, error) {
	createdTask, err := s.model.CreateTask(ctx, querier.CreateTaskParams{
		Attributes: task.Attributes,
		Spec:       task.Spec,
		StartedAt:  task.StartedAt,
		Status:     string(task.Status),
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to create task")
	}
	return createdTask.ID, nil
}

func (s *TaskStore) UpdateCronJob(ctx context.Context, taskID int32, cronExpression string, spec json.RawMessage) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cron, err := parser.Parse(cronExpression)
	if err != nil {
		return errors.Wrapf(err, "failed to parse cron expression")
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

func (s *TaskStore) PauseCronJob(ctx context.Context, taskID int32) error {
	if err := s.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Paused),
	}); err != nil {
		return errors.Wrapf(err, "failed to pause task")
	}
	return nil
}

func (s *TaskStore) ResumeCronJob(ctx context.Context, taskID int32) error {
	if err := s.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Pending),
	}); err != nil {
		return errors.Wrapf(err, "failed to resume task")
	}
	return nil
}
