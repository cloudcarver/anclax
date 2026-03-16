package asynctask

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/pgnotify"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pkg/errors"
)

type pgxConn interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	WaitForNotification(ctx context.Context) (*pgconn.Notification, error)
	Close(ctx context.Context) error
}

var connectPgx = func(ctx context.Context, dsn string) (pgxConn, error) {
	return pgx.Connect(ctx, dsn)
}

func (e *Executor) ExecuteInterruptTask(ctx context.Context, _ worker.Task, params *taskgen.InterruptTaskParameters) error {
	if params == nil {
		return errors.Wrap(taskcore.ErrFatalTask, "interrupt task params cannot be nil")
	}
	if len(params.TaskIDs) == 0 {
		return errors.Wrap(taskcore.ErrFatalTask, "taskIDs must be non-empty")
	}

	taskIDs := make([]int32, 0, len(params.TaskIDs))
	seen := make(map[int32]struct{}, len(params.TaskIDs))
	for _, taskID := range params.TaskIDs {
		if taskID <= 0 {
			return errors.Wrap(taskcore.ErrFatalTask, "taskIDs must be positive")
		}
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		taskIDs = append(taskIDs, taskID)
	}
	if len(taskIDs) == 0 {
		return errors.Wrap(taskcore.ErrFatalTask, "taskIDs must be non-empty")
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

	notifyRaw, err := json.Marshal(pgnotify.TaskInterruptNotification{
		Op: pgnotify.OpInterruptTask,
		Params: pgnotify.TaskInterruptParams{
			RequestID: requestID,
			TaskIDs:   taskIDs,
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
	ackConn, err := connectPgx(ctx, e.runtimeListenDSN)
	if err != nil {
		return errors.Wrap(err, "connect task interrupt ack listener")
	}
	defer ackConn.Close(context.Background())

	if _, err := ackConn.Exec(ctx, fmt.Sprintf("LISTEN %s", pgnotify.ChannelTaskInterruptAck)); err != nil {
		return errors.Wrap(err, "listen task interrupt ack channel")
	}

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

		if err := waitForTaskInterruptAcks(ctx, ackConn, requestID, acked, listenTimeout); err != nil {
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

func waitForTaskInterruptAcks(ctx context.Context, conn pgxConn, requestID string, acked map[uuid.UUID]struct{}, listenTimeout time.Duration) error {
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
	var ack pgnotify.TaskInterruptAckNotification
	if err := json.Unmarshal([]byte(payload), &ack); err != nil {
		return uuid.Nil, false
	}
	if !pgnotify.MatchesOp(ack.Op, pgnotify.OpAck) {
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
