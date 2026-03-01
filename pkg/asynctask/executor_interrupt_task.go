package asynctask

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
)

const taskInterruptAckChannel = "anclax_worker_task_interrupt_ack"

const taskInterruptOp = "interrupt_task"

type taskInterruptNotification struct {
	Op     string `json:"op"`
	Params struct {
		RequestID string `json:"request_id"`
		TaskID    int32  `json:"task_id"`
	} `json:"params"`
}

type taskInterruptAckNotification struct {
	Op     string `json:"op"`
	Params struct {
		RequestID string `json:"request_id"`
		WorkerID  string `json:"worker_id"`
	} `json:"params"`
}

func (e *Executor) ExecuteInterruptTask(ctx context.Context, params *taskgen.InterruptTaskParameters) error {
	if params == nil {
		return errors.Wrap(taskcore.ErrFatalTask, "interrupt task params cannot be nil")
	}
	if params.TaskID <= 0 {
		return errors.Wrap(taskcore.ErrFatalTask, "taskID must be positive")
	}

	requestID := ""
	if params.RequestID != nil && *params.RequestID != "" {
		requestID = *params.RequestID
	} else {
		requestID = uuid.NewString()
	}
	notifyInterval, listenTimeout, err := parseTaskInterruptDurations(params)
	if err != nil {
		return errors.Wrap(taskcore.ErrFatalTask, err.Error())
	}

	notifyRaw, err := json.Marshal(taskInterruptNotification{
		Op: taskInterruptOp,
		Params: struct {
			RequestID string `json:"request_id"`
			TaskID    int32  `json:"task_id"`
		}{
			RequestID: requestID,
			TaskID:    params.TaskID,
		},
	})
	if err != nil {
		return errors.Wrap(err, "marshal task interrupt notification payload")
	}

	if e.runtimeListenDSN == "" {
		return errors.New("task interrupt requires pg dsn for LISTEN ack")
	}

	heartbeatTTL := e.runtimeConfigHeartbeatTTL
	if heartbeatTTL <= 0 {
		heartbeatTTL = defaultWorkerHeartbeatInterval * runtimeConfigHeartbeatTTLMultiplier
	}

	acked := map[uuid.UUID]struct{}{}
	for {
		online, err := e.model.ListOnlineWorkerIDs(ctx, e.now().Add(-heartbeatTTL))
		if err != nil {
			return errors.Wrap(err, "list online workers")
		}
		if len(online) == 0 {
			return nil
		}
		if allWorkersAcked(online, acked) {
			return nil
		}

		if err := e.model.NotifyWorkerTaskInterrupt(ctx, string(notifyRaw)); err != nil {
			return errors.Wrap(err, "notify worker task interrupt")
		}

		if err := waitForTaskInterruptAcks(ctx, e.runtimeListenDSN, requestID, acked, listenTimeout); err != nil {
			return errors.Wrap(err, "wait for task interrupt ack")
		}
		if allWorkersAcked(online, acked) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(notifyInterval):
		}
	}
}

func allWorkersAcked(workers []uuid.UUID, acked map[uuid.UUID]struct{}) bool {
	for _, workerID := range workers {
		if _, ok := acked[workerID]; !ok {
			return false
		}
	}
	return true
}

func waitForTaskInterruptAcks(ctx context.Context, dsn string, requestID string, acked map[uuid.UUID]struct{}, listenTimeout time.Duration) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", taskInterruptAckChannel)); err != nil {
		return err
	}

	waitCtx, cancel := context.WithTimeout(ctx, listenTimeout)
	defer cancel()
	for {
		notification, err := conn.WaitForNotification(waitCtx)
		if err != nil {
			if stdErrors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			if stdErrors.Is(err, context.Canceled) && ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		workerID, ok := taskInterruptAckWorker(notification.Payload, requestID)
		if ok {
			acked[workerID] = struct{}{}
		}
	}
}

func taskInterruptAckWorker(payload string, requestID string) (uuid.UUID, bool) {
	if requestID == "" {
		return uuid.Nil, false
	}
	var ack taskInterruptAckNotification
	if err := json.Unmarshal([]byte(payload), &ack); err != nil {
		return uuid.Nil, false
	}
	if ack.Op != "" && ack.Op != "ack" {
		return uuid.Nil, false
	}
	if ack.Params.RequestID != requestID || ack.Params.WorkerID == "" {
		return uuid.Nil, false
	}
	workerID, err := uuid.Parse(ack.Params.WorkerID)
	if err != nil {
		return uuid.Nil, false
	}
	return workerID, true
}

func parseTaskInterruptDurations(params *taskgen.InterruptTaskParameters) (notifyInterval time.Duration, listenTimeout time.Duration, retErr error) {
	notifyInterval = time.Second
	listenTimeout = 2 * time.Second

	if params.NotifyInterval != nil {
		notifyInterval, retErr = time.ParseDuration(*params.NotifyInterval)
		if retErr != nil {
			return 0, 0, errors.Wrap(retErr, "invalid notifyInterval duration")
		}
	}

	if params.ListenTimeout != nil {
		listenTimeout, retErr = time.ParseDuration(*params.ListenTimeout)
		if retErr != nil {
			return 0, 0, errors.Wrap(retErr, "invalid listenTimeout duration")
		}
	}

	if notifyInterval <= 0 || listenTimeout <= 0 {
		return 0, 0, errors.New("notifyInterval and listenTimeout must be positive")
	}
	return notifyInterval, listenTimeout, nil
}
