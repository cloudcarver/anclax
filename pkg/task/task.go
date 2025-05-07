package task

import (
	"encoding/json"
	"time"

	"github.com/cloudcarver/anchor/pkg/apigen"
	"github.com/cloudcarver/anchor/pkg/model"
	"github.com/cloudcarver/anchor/pkg/model/querier"
	"github.com/pkg/errors"
)

type TaskStore struct {
	now func() time.Time
}

func NewTaskStore() TaskStoreInterface {
	return &TaskStore{
		now: time.Now,
	}
}

type CreateTaskParams struct {
	OrgID                *int32
	Spec                 json.RawMessage
	StartedAt            *time.Time
	AlwaysRetryOnFailure bool
	RetryInterval        string
}

type CreateScheduledTaskParams struct {
	OrgID                *int32
	Spec                 json.RawMessage
	StartedAt            *time.Time
	AlwaysRetryOnFailure bool
}

func (s *TaskStore) PushTask(c *model.Context, task *apigen.Task) (int32, error) {
	createdTask, err := c.CreateTask(c, querier.CreateTaskParams{
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
