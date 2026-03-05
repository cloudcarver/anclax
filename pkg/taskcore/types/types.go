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
	attributes := task.Attributes
	if attributes.Priority == nil {
		priority := task.Priority
		attributes.Priority = &priority
	}
	if attributes.Weight == nil {
		weight := task.Weight
		attributes.Weight = &weight
	}
	return apigen.Task{
		ID:           task.ID,
		ParentTaskId: task.ParentTaskID,
		CreatedAt:    task.CreatedAt,
		Spec:         task.Spec,
		StartedAt:    task.StartedAt,
		LockedAt:     task.LockedAt,
		WorkerId:     workerID,
		Status:       apigen.TaskStatus(task.Status),
		UpdatedAt:    task.UpdatedAt,
		Attempts:     task.Attempts,
		Attributes:   attributes,
	}
}
