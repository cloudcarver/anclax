package service

import (
	"context"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/pkg/errors"
)

func taskToApiTask(task *querier.AnclaxTask) *apigen.Task {
	return &apigen.Task{
		ID:         task.ID,
		Attributes: task.Attributes,
		Spec:       task.Spec,
		Status:     apigen.TaskStatus(task.Status),
		StartedAt:  task.StartedAt,
		CreatedAt:  task.CreatedAt,
		UpdatedAt:  task.UpdatedAt,
		UniqueTag:  task.UniqueTag,
	}
}

func (s *Service) ListTasks(ctx context.Context) ([]apigen.Task, error) {
	return nil, errors.New("not implemented")
}

func (s *Service) ListEvents(ctx context.Context) ([]apigen.Event, error) {
	return nil, errors.New("not implemented")
}

func (s *Service) GetTaskByID(ctx context.Context, id int32) (*apigen.Task, error) {
	task, err := s.m.GetTaskByID(ctx, id)
	if err != nil {
		return nil, err
	}

	return taskToApiTask(task), nil
}

func (s *Service) TryExecuteTask(ctx context.Context, id int32) error {
	return s.worker.RunTask(ctx, id)
}
