package task

import (
	"encoding/json"

	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/model/querier"
	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
)

func (s *TaskStore) UpdateCronJob(c *model.Context, taskID int32, cronExpression string, spec json.RawMessage) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	cron, err := parser.Parse(cronExpression)
	if err != nil {
		return errors.Wrapf(err, "failed to parse cron expression")
	}
	nextTime := cron.Next(s.now())

	task, err := c.GetTaskByID(c, taskID)
	if err != nil {
		return errors.Wrapf(err, "failed to get task")
	}

	task.Attributes.Cronjob = &apigen.TaskCronjob{
		CronExpression: cronExpression,
	}

	task.Spec.Payload = spec

	if err := c.UpdateTask(c, querier.UpdateTaskParams{
		ID:         taskID,
		Attributes: task.Attributes,
		StartedAt:  &nextTime,
		Spec:       task.Spec,
	}); err != nil {
		return errors.Wrapf(err, "failed to update task")
	}
	return nil
}

func (s *TaskStore) PauseCronJob(c *model.Context, taskID int32) error {
	if err := c.UpdateTaskStatus(c, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Paused),
	}); err != nil {
		return errors.Wrapf(err, "failed to pause task")
	}
	return nil
}

func (s *TaskStore) ResumeCronJob(c *model.Context, taskID int32) error {
	if err := c.UpdateTaskStatus(c, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Pending),
	}); err != nil {
		return errors.Wrapf(err, "failed to resume task")
	}
	return nil
}
