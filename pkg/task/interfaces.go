package task

import (
	"context"
	"encoding/json"

	"github.com/cloudcarver/anchor/pkg/apigen"
)

type TaskStoreInterface interface {
	PushTask(ctx context.Context, task *apigen.Task) (int32, error)

	UpdateCronJob(ctx context.Context, taskID int32, cronExpression string, spec json.RawMessage) error

	PauseCronJob(ctx context.Context, taskID int32) error

	ResumeCronJob(ctx context.Context, taskID int32) error
}
