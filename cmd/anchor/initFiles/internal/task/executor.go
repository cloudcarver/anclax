package task

import "context"

type Executor struct {
}

func NewExecutor() ExecutorInterface {
	return &Executor{}
}

func (e *Executor) IncrementCounter(ctx context.Context, params *IncrementCounterParameters) error {
	return nil
}
