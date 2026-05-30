package listener

import (
	"context"
	"errors"

	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
)

var (
	ErrTaskNotFound   = errors.New("task not found")
	ErrListenerClosed = errors.New("task listener closed")
)

type TaskTerminalEvent struct {
	TaskID int32
	Status apigen.TaskStatus
	Err    error
}

type TaskEventListener interface {
	WaitTask(ctx context.Context, taskID int32) (<-chan TaskTerminalEvent, error)
}

func IsTerminalStatus(status apigen.TaskStatus) bool {
	switch status {
	case apigen.Completed, apigen.Failed, apigen.Cancelled:
		return true
	default:
		return false
	}
}
