package runner

type Executor struct {
}

func NewExecutor() ExecutorInterface {
	return &Executor{}
}

func (e *Executor) DeleteOpaqueKey(params *DeleteOpaqueKeyParameters) error {
	return nil
}
