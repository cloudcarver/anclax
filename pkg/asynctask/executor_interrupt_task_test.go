package asynctask

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore/pgnotify"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type fakeInterruptAckConn struct {
	listened  atomic.Bool
	waitCalls atomic.Int32
	payload   string
}

func (c *fakeInterruptAckConn) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.HasPrefix(strings.TrimSpace(sql), "LISTEN") {
		c.listened.Store(true)
	}
	return pgconn.CommandTag{}, nil
}

func (c *fakeInterruptAckConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	if !c.listened.Load() {
		return nil, context.Canceled
	}
	if c.waitCalls.Add(1) == 1 {
		return &pgconn.Notification{Payload: c.payload}, nil
	}
	return nil, context.DeadlineExceeded
}

func (c *fakeInterruptAckConn) Close(ctx context.Context) error {
	return nil
}

func TestExecuteInterruptTaskListenBeforeNotify(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workerID := uuid.New()
	requestID := "req-ack"
	listenTimeout := "10ms"

	ack := pgnotify.TaskInterruptAckNotification{Op: pgnotify.OpAck}
	ack.Params.RequestID = requestID
	ack.Params.WorkerID = workerID.String()
	ackRaw, err := json.Marshal(ack)
	require.NoError(t, err)

	fakeConn := &fakeInterruptAckConn{payload: string(ackRaw)}
	prevConnect := connectPgx
	connectPgx = func(ctx context.Context, dsn string) (pgxConn, error) {
		return fakeConn, nil
	}
	defer func() { connectPgx = prevConnect }()

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{workerID}, nil)
	mockModel.EXPECT().NotifyWorkerTaskInterrupt(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, payload string) error {
			require.True(t, fakeConn.listened.Load(), "ack listener should be active before notify")
			var parsed pgnotify.TaskInterruptNotification
			require.NoError(t, json.Unmarshal([]byte(payload), &parsed))
			require.Equal(t, requestID, parsed.Params.RequestID)
			require.Equal(t, int32(42), parsed.Params.TaskID)
			return nil
		},
	)

	exec := &Executor{
		model:                     mockModel,
		now:                       time.Now,
		runtimeListenDSN:          "postgres://example",
		runtimeConfigHeartbeatTTL: 5 * time.Second,
	}

	err = exec.ExecuteInterruptTask(context.Background(), &taskgen.InterruptTaskParameters{
		TaskID:        42,
		RequestID:     &requestID,
		ListenTimeout: &listenTimeout,
	})
	require.NoError(t, err)
}

func TestExecuteInterruptTaskRequiresDSN(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	exec := &Executor{model: model.NewMockModelInterface(ctrl), now: time.Now}
	err := exec.ExecuteInterruptTask(context.Background(), &taskgen.InterruptTaskParameters{TaskID: 1})
	require.Error(t, err)
	require.ErrorContains(t, err, "requires pg dsn")
}

func TestExecuteInterruptTaskNoOnlineWorkers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	fakeConn := &fakeInterruptAckConn{}
	prevConnect := connectPgx
	connectPgx = func(ctx context.Context, dsn string) (pgxConn, error) {
		return fakeConn, nil
	}
	defer func() { connectPgx = prevConnect }()

	mockModel := model.NewMockModelInterface(ctrl)
	mockModel.EXPECT().ListOnlineWorkerIDs(gomock.Any(), gomock.Any()).Return([]uuid.UUID{}, nil)
	mockModel.EXPECT().NotifyWorkerTaskInterrupt(gomock.Any(), gomock.Any()).Times(0)

	exec := &Executor{
		model:                     mockModel,
		now:                       time.Now,
		runtimeListenDSN:          "postgres://example",
		runtimeConfigHeartbeatTTL: 5 * time.Second,
	}

	err := exec.ExecuteInterruptTask(context.Background(), &taskgen.InterruptTaskParameters{TaskID: 9})
	require.NoError(t, err)
	require.Equal(t, int32(0), fakeConn.waitCalls.Load())
}

func TestExecuteInterruptTaskInvalidDurations(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	exec := &Executor{
		model:            model.NewMockModelInterface(ctrl),
		now:              time.Now,
		runtimeListenDSN: "postgres://example",
	}

	invalid := "bad"
	err := exec.ExecuteInterruptTask(context.Background(), &taskgen.InterruptTaskParameters{
		TaskID:         7,
		NotifyInterval: &invalid,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "invalid notifyInterval duration")
	require.True(t, errors.Is(err, taskcore.ErrFatalTask))
}
