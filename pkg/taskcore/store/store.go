package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/types"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
)

var (
	ErrTaskNotFound      = errors.New("task not found")
	ErrTaskEventNotFound = errors.New("task event not found")
)

type TaskStore struct {
	now func() time.Time

	model model.ModelInterface
}

// NewTaskStore returns a TaskStore backed by the provided model and default time source.
// It uses time.Now for scheduling decisions and the given model for persistence.
// Callers typically keep a single instance and derive transaction-scoped stores via WithTx.
func NewTaskStore(model model.ModelInterface) TaskStoreInterface {
	return &TaskStore{
		now:   time.Now,
		model: model,
	}
}

// WithTx returns a TaskStore bound to the given transaction while keeping the same time source.
// The returned store reads/writes through the provided transaction context.
// Callers are responsible for committing or rolling back the transaction.
func (s *TaskStore) WithTx(tx core.Tx) TaskStoreInterface {
	return &TaskStore{
		now:   s.now,
		model: s.model.SpawnWithTx(tx),
	}
}

// PushTask inserts a task and returns its ID.
// If task.UniqueTag is set and a matching task exists, it returns the existing ID without inserting.
// The task's attributes, spec, status, started_at, and unique tag are persisted as provided.
func (s *TaskStore) PushTask(ctx context.Context, task *apigen.Task) (int32, error) {
	if task.UniqueTag != nil {
		task, err := s.model.GetTaskByUniqueTag(ctx, task.UniqueTag)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return 0, errors.Wrap(err, "failed to check task by unique tag before push")
			}
		} else {
			return task.ID, nil
		}
	}
	serialKey, serialID, err := serialAttributesFromJSON(task.Attributes)
	if err != nil {
		return 0, err
	}
	priority, weight, err := priorityAndWeightAttributes(task.Attributes)
	if err != nil {
		return 0, err
	}
	task.Attributes.Priority = utils.Ptr(priority)
	task.Attributes.Weight = utils.Ptr(weight)

	createdTask, err := s.model.CreateTask(ctx, querier.CreateTaskParams{
		Attributes: task.Attributes,
		Spec:       task.Spec,
		StartedAt:  task.StartedAt,
		Status:     string(task.Status),
		UniqueTag:  task.UniqueTag,
		SerialKey:  serialKey,
		SerialID:   serialID,
		Priority:   priority,
		Weight:     weight,
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to push task")
	}
	return createdTask.ID, nil
}

// UpdateCronJob updates a task's cron expression and payload, and schedules the next run time.
// It parses the cron expression, computes the next fire time based on the store clock,
// and persists the updated cron metadata, payload, and started_at timestamp.
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
	serialKey, serialID, err := serialAttributesFromJSON(task.Attributes)
	if err != nil {
		return err
	}
	priority, weight, err := priorityAndWeightAttributes(task.Attributes)
	if err != nil {
		return err
	}
	task.Attributes.Priority = utils.Ptr(priority)
	task.Attributes.Weight = utils.Ptr(weight)

	if err := s.model.UpdateTask(ctx, querier.UpdateTaskParams{
		ID:         taskID,
		Attributes: task.Attributes,
		StartedAt:  &nextTime,
		Spec:       task.Spec,
		SerialKey:  serialKey,
		SerialID:   serialID,
		Priority:   priority,
		Weight:     weight,
	}); err != nil {
		return errors.Wrapf(err, "failed to update task")
	}
	return nil
}

// PauseTask marks a task as paused so workers will stop picking it up.
// It updates the task status to Paused in storage.
func (s *TaskStore) PauseTask(ctx context.Context, taskID int32) error {
	if err := s.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Paused),
	}); err != nil {
		return errors.Wrapf(err, "failed to pause task")
	}
	return nil
}

// ResumeTask marks a paused task as pending to make it eligible for execution again.
// It updates the task status to Pending in storage.
func (s *TaskStore) ResumeTask(ctx context.Context, taskID int32) error {
	if err := s.model.UpdateTaskStatus(ctx, querier.UpdateTaskStatusParams{
		ID:     taskID,
		Status: string(apigen.Pending),
	}); err != nil {
		return errors.Wrapf(err, "failed to resume task")
	}
	return nil
}

func (s *TaskStore) UpdatePendingTaskPriorityByLabels(ctx context.Context, labels []string, priority int32) (int64, error) {
	if priority < 0 {
		return 0, errors.New("priority must be non-negative")
	}
	ret, err := s.model.UpdatePendingTaskPriorityByLabels(ctx, querier.UpdatePendingTaskPriorityByLabelsParams{
		Priority:  priority,
		HasLabels: len(labels) > 0,
		Labels:    labels,
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to update pending task priority by labels")
	}
	return ret, nil
}

func (s *TaskStore) UpdatePendingTaskWeightByLabels(ctx context.Context, labels []string, weight int32) (int64, error) {
	if weight < 1 {
		return 0, errors.New("weight must be greater than or equal to 1")
	}
	ret, err := s.model.UpdatePendingTaskWeightByLabels(ctx, querier.UpdatePendingTaskWeightByLabelsParams{
		Weight:    weight,
		HasLabels: len(labels) > 0,
		Labels:    labels,
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to update pending task weight by labels")
	}
	return ret, nil
}

// GetTaskByUniqueTag returns a task by unique tag.
// It maps the stored model to apigen.Task and returns ErrTaskNotFound when absent.
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

// GetTaskByID returns a task by ID.
// It maps the stored model to apigen.Task and returns ErrTaskNotFound when absent.
func (s *TaskStore) GetTaskByID(ctx context.Context, taskID int32) (*apigen.Task, error) {
	task, err := s.model.GetTaskByID(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, errors.Wrap(err, "failed to get task")
	}
	ret := types.TaskToAPI(task)
	return &ret, nil
}

// GetLastTaskErrorEvent returns the most recent TaskError event for a task.
// It returns ErrTaskEventNotFound when the task has no error events.
func (s *TaskStore) GetLastTaskErrorEvent(ctx context.Context, taskID int32) (*apigen.Event, error) {
	event, err := s.model.GetLastTaskErrorEvent(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTaskEventNotFound
		}
		return nil, errors.Wrap(err, "failed to get last task error event")
	}
	return &apigen.Event{
		ID:        event.ID,
		Spec:      event.Spec,
		CreatedAt: event.CreatedAt,
	}, nil
}

func serialAttributes(attributes apigen.TaskAttributes) (*string, *int32, error) {
	if attributes.SerialKey == nil && attributes.SerialID == nil {
		return nil, nil, nil
	}
	if attributes.SerialID != nil && attributes.SerialKey == nil {
		return nil, nil, errors.New("serialID requires serialKey")
	}
	if attributes.SerialKey != nil && *attributes.SerialKey == "" {
		return nil, nil, errors.New("serialKey cannot be empty")
	}
	return attributes.SerialKey, attributes.SerialID, nil
}

func serialAttributesFromJSON(attributes apigen.TaskAttributes) (*string, *int32, error) {
	if attributes.SerialKey != nil || attributes.SerialID != nil {
		return serialAttributes(attributes)
	}

	raw, err := json.Marshal(attributes)
	if err != nil {
		return nil, nil, errors.Wrap(err, "marshal task attributes")
	}
	var serial struct {
		SerialKey *string `json:"serialKey"`
		SerialID  *int32  `json:"serialID"`
	}
	if err := json.Unmarshal(raw, &serial); err != nil {
		return nil, nil, errors.Wrap(err, "unmarshal task serial attributes")
	}
	return serialAttributes(apigen.TaskAttributes{
		SerialKey: serial.SerialKey,
		SerialID:  serial.SerialID,
	})
}

func priorityAndWeightAttributes(attributes apigen.TaskAttributes) (int32, int32, error) {
	priority := int32(0)
	if attributes.Priority != nil {
		priority = *attributes.Priority
	}
	if priority < 0 {
		return 0, 0, errors.New("priority must be non-negative")
	}

	weight := int32(1)
	if attributes.Weight != nil {
		weight = *attributes.Weight
	}
	if weight < 1 {
		return 0, 0, errors.New("weight must be greater than or equal to 1")
	}
	return priority, weight, nil
}
