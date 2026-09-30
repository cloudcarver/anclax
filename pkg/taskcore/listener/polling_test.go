package listener

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWaitTaskRegistersWithoutQueryAndBatchesResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := model.NewMockModelInterface(gomock.NewController(t))
		l := NewPollingTaskEventListener(m)
		defer l.Close(context.Background())
		channels := make([]<-chan TaskTerminalEvent, 0)
		for _, id := range []int32{1, 2, 3, 4, 1} {
			ch, err := l.WaitTask(context.Background(), id)
			require.NoError(t, err)
			assertNoListenerEvent(t, ch)
			channels = append(channels, ch)
		}
		l.mu.Lock()
		m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1, 2, 3, 4}).Return([]*querier.ListTaskWaitStatusesRow{
			{ID: 1, Status: "completed"}, {ID: 2, Status: "failed"}, {ID: 3, Status: "cancelled"},
		}, nil)
		l.mu.Unlock()
		time.Sleep(defaultPollInterval)
		synctest.Wait()
		for i, status := range []apigen.TaskStatus{apigen.Completed, apigen.Failed, apigen.Cancelled, "", apigen.Completed} {
			event := readListenerEvent(t, channels[i])
			require.Equal(t, status, event.Status)
			if i == 3 {
				require.ErrorIs(t, event.Err, ErrTaskNotFound)
			} else {
				require.NoError(t, event.Err)
			}
			_, open := <-channels[i]
			require.False(t, open)
		}
		time.Sleep(defaultPollInterval) // No watchers: no further database call.
	})
}

func TestListenerRetriesTransientFailuresWithBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := model.NewMockModelInterface(gomock.NewController(t))
		l := NewPollingTaskEventListener(m)
		defer l.Close(context.Background())
		ch, err := l.WaitTask(context.Background(), 1)
		require.NoError(t, err)
		l.mu.Lock()
		gomock.InOrder(
			m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).Return(nil, errors.New("connection reset")),
			m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).Return(nil, &pgconn.PgError{Code: "57P01"}),
			m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).Return([]*querier.ListTaskWaitStatusesRow{{ID: 1, Status: "pending"}}, nil),
			m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).Return([]*querier.ListTaskWaitStatusesRow{{ID: 1, Status: "completed"}}, nil),
		)
		l.mu.Unlock()
		for _, delay := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
			time.Sleep(delay)
			synctest.Wait()
			assertNoListenerEvent(t, ch)
		}
		time.Sleep(time.Second) // Successful pending observation resets backoff.
		synctest.Wait()
		require.Equal(t, apigen.Completed, readListenerEvent(t, ch).Status)
	})
}

func TestListenerQueryTimeoutPreservesSubscription(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := model.NewMockModelInterface(gomock.NewController(t))
		l := NewPollingTaskEventListener(m)
		defer l.Close(context.Background())
		ch, err := l.WaitTask(context.Background(), 1)
		require.NoError(t, err)
		l.mu.Lock()
		m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).DoAndReturn(func(ctx context.Context, _ []int32) ([]*querier.ListTaskWaitStatusesRow, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Equal(t, defaultQueryTimeout, time.Until(deadline))
			<-ctx.Done()
			return nil, ctx.Err()
		})
		l.mu.Unlock()
		time.Sleep(defaultPollInterval + defaultQueryTimeout)
		synctest.Wait()
		assertNoListenerEvent(t, ch)
	})
}

func TestListenerReportsPermanentQueryErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := model.NewMockModelInterface(gomock.NewController(t))
		l := NewPollingTaskEventListener(m)
		defer l.Close(context.Background())
		ch, err := l.WaitTask(context.Background(), 1)
		require.NoError(t, err)
		queryErr := &pgconn.PgError{Code: "42501", Message: "permission denied"}
		l.mu.Lock()
		m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).Return(nil, queryErr)
		l.mu.Unlock()
		time.Sleep(defaultPollInterval)
		require.ErrorIs(t, readListenerEvent(t, ch).Err, queryErr)
	})
}

func TestListenerBoundsBatchSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := model.NewMockModelInterface(gomock.NewController(t))
		l := NewPollingTaskEventListener(m)
		defer l.Close(context.Background())
		var channels []<-chan TaskTerminalEvent
		for id := int32(1); id <= defaultBatchSize*2+1; id++ {
			ch, err := l.WaitTask(context.Background(), id)
			require.NoError(t, err)
			channels = append(channels, ch)
		}
		var sizes []int
		l.mu.Lock()
		m.EXPECT().ListTaskWaitStatuses(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, ids []int32) ([]*querier.ListTaskWaitStatusesRow, error) {
			sizes = append(sizes, len(ids))
			var rows []*querier.ListTaskWaitStatusesRow
			for _, id := range ids {
				rows = append(rows, &querier.ListTaskWaitStatusesRow{ID: id, Status: "completed"})
			}
			return rows, nil
		}).Times(3)
		l.mu.Unlock()
		time.Sleep(defaultPollInterval)
		synctest.Wait()
		require.Equal(t, []int{defaultBatchSize, defaultBatchSize, 1}, sizes)
		for _, ch := range channels {
			require.NoError(t, readListenerEvent(t, ch).Err)
		}
	})
}

func TestListenerCancellationAndClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewPollingTaskEventListener(model.NewMockModelInterface(gomock.NewController(t)))
		ctx, cancel := context.WithCancel(context.Background())
		ch, err := l.WaitTask(ctx, 1)
		require.NoError(t, err)
		cancel()
		synctest.Wait()
		_, open := <-ch
		require.False(t, open)
		ids, _ := l.snapshot()
		require.Empty(t, ids)
		_, err = l.WaitTask(ctx, 1)
		require.ErrorIs(t, err, context.Canceled)
		ch, err = l.WaitTask(context.Background(), 2)
		require.NoError(t, err)
		require.NoError(t, l.Close(context.Background()))
		require.ErrorIs(t, readListenerEvent(t, ch).Err, ErrListenerClosed)
		_, err = l.WaitTask(context.Background(), 3)
		require.ErrorIs(t, err, ErrListenerClosed)
	})
}

func TestListenerDoesNotNotifyNewSubscriberWithStaleResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := model.NewMockModelInterface(gomock.NewController(t))
		l := NewPollingTaskEventListener(m)
		defer l.Close(context.Background())
		first, err := l.WaitTask(context.Background(), 1)
		require.NoError(t, err)
		queryStarted, releaseQuery := make(chan struct{}), make(chan struct{})
		l.mu.Lock()
		m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).DoAndReturn(func(context.Context, []int32) ([]*querier.ListTaskWaitStatusesRow, error) {
			close(queryStarted)
			<-releaseQuery
			return nil, nil
		})
		l.mu.Unlock()
		<-queryStarted
		second, err := l.WaitTask(context.Background(), 1)
		require.NoError(t, err)
		close(releaseQuery)
		require.ErrorIs(t, readListenerEvent(t, first).Err, ErrTaskNotFound)
		assertNoListenerEvent(t, second)
		l.mu.Lock()
		m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).Return([]*querier.ListTaskWaitStatusesRow{{ID: 1, Status: "completed"}}, nil)
		l.mu.Unlock()
		time.Sleep(defaultPollInterval)
		require.Equal(t, apigen.Completed, readListenerEvent(t, second).Status)
	})
}

func TestCloseCancelsInFlightListenerQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := model.NewMockModelInterface(gomock.NewController(t))
		l := NewPollingTaskEventListener(m)
		ch, err := l.WaitTask(context.Background(), 1)
		require.NoError(t, err)
		started := make(chan struct{})
		l.mu.Lock()
		m.EXPECT().ListTaskWaitStatuses(gomock.Any(), []int32{1}).DoAndReturn(func(ctx context.Context, _ []int32) ([]*querier.ListTaskWaitStatusesRow, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		l.mu.Unlock()
		<-started
		require.NoError(t, l.Close(context.Background()))
		require.ErrorIs(t, readListenerEvent(t, ch).Err, ErrListenerClosed)
	})
}

func assertNoListenerEvent(t *testing.T, ch <-chan TaskTerminalEvent) {
	t.Helper()
	select {
	case event := <-ch:
		t.Fatalf("unexpected event: %+v", event)
	default:
	}
}

func readListenerEvent(t *testing.T, ch <-chan TaskTerminalEvent) TaskTerminalEvent {
	t.Helper()
	select {
	case event, ok := <-ch:
		require.True(t, ok)
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for listener event")
		return TaskTerminalEvent{}
	}
}
