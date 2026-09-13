package store

import (
	"fmt"
	"time"
)

// TaskDeferred suspends an unfinished task without recording an error or
// consuming a retry attempt. The next invocation must recover its progress
// from durable state, using stable idempotency keys for any child tasks.
type TaskDeferred struct {
	Delay time.Duration
}

func (e *TaskDeferred) Error() string { return fmt.Sprintf("task deferred for %s", e.Delay) }

func DeferTask(delay time.Duration) error {
	if delay < 0 {
		return fmt.Errorf("%w: task deferral delay must be non-negative", ErrFatalTask)
	}
	return &TaskDeferred{Delay: delay}
}
