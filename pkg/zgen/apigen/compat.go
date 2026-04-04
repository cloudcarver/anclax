package apigen

const (
	Bearer = CredentialsTokenTypeBearer

	TaskError     = EventSpecTypeTaskError
	TaskCompleted = EventSpecTypeTaskCompleted

	OnFailed = TaskEventsOnFailed

	Pending   = TaskStatusPending
	Completed = TaskStatusCompleted
	Failed    = TaskStatusFailed
	Paused    = TaskStatusPaused
	Cancelled = TaskStatusCancelled
)
