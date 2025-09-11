package types

import (
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
)

func TaskToAPI(task *querier.AnchorTask) apigen.Task {
	return apigen.Task{
		ID:         task.ID,
		CreatedAt:  task.CreatedAt,
		Spec:       task.Spec,
		StartedAt:  task.StartedAt,
		Status:     apigen.TaskStatus(task.Status),
		UpdatedAt:  task.UpdatedAt,
		Attempts:   task.Attempts,
		Attributes: task.Attributes,
	}
}
