package types

import (
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
)

func TaskToAPI(task *querier.AnclaxTask) apigen.Task {
	var workerID *string
	if task.WorkerID.Valid {
		id := task.WorkerID.UUID.String()
		workerID = &id
	}
	return apigen.Task{
		ID:         task.ID,
		CreatedAt:  task.CreatedAt,
		Spec:       task.Spec,
		StartedAt:  task.StartedAt,
		LockedAt:   task.LockedAt,
		WorkerId:   workerID,
		Status:     apigen.TaskStatus(task.Status),
		UpdatedAt:  task.UpdatedAt,
		Attempts:   task.Attempts,
		Attributes: task.Attributes,
	}
}
