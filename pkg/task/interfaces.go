package task

import (
	"encoding/json"

	"github.com/cloudcarver/anchor/pkg/apigen"
	"github.com/cloudcarver/anchor/pkg/model"
)

type TaskStoreInterface interface {
	PushTask(c *model.Context, task *apigen.Task) (int32, error)

	UpdateCronJob(c *model.Context, taskID int32, cronExpression string, spec json.RawMessage) error

	PauseCronJob(c *model.Context, taskID int32) error

	ResumeCronJob(c *model.Context, taskID int32) error
}
