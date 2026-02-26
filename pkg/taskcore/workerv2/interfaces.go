package workerv2

import (
	"context"

	legacyworker "github.com/cloudcarver/anclax/pkg/taskcore/worker"
)

// TaskHandler is intentionally aliased to the legacy handler interface so
// existing handlers can be reused without adapter code changes.
type TaskHandler = legacyworker.TaskHandler

type WorkerInterface interface {
	Start()
	RunTask(ctx context.Context, taskID int32) error
	RegisterTaskHandler(handler TaskHandler)
}
