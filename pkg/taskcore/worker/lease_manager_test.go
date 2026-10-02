package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type leaseTestQueries struct {
	refresh func(context.Context, querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error)
	status  func(context.Context, []int32) ([]*querier.ListTaskWaitStatusesRow, error)
}

func (q leaseTestQueries) RefreshTaskLocks(ctx context.Context, p querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error) {
	return q.refresh(ctx, p)
}
func (q leaseTestQueries) ListTaskWaitStatuses(ctx context.Context, ids []int32) ([]*querier.ListTaskWaitStatusesRow, error) {
	return q.status(ctx, ids)
}

func renewedRows(p querier.RefreshTaskLocksParams) []*querier.RefreshTaskLocksRow {
	var rows []*querier.RefreshTaskLocksRow
	for i, id := range p.Ids {
		rows = append(rows, &querier.RefreshTaskLocksRow{ID: id, LeaseVersion: p.LeaseVersions[i]})
	}
	return rows
}

func TestLeaseManagerBatchesAndBoundsConcurrentQueries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var active, peak, calls atomic.Int32
		q := leaseTestQueries{refresh: func(ctx context.Context, p querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error) {
			n := active.Add(1)
			defer active.Add(-1)
			peak.Store(max(peak.Load(), n))
			calls.Add(1)
			require.LessOrEqual(t, len(p.Ids), leaseRenewalBatchSize)
			require.Len(t, p.LeaseVersions, len(p.Ids))
			time.Sleep(100 * time.Millisecond)
			return renewedRows(p), nil
		}}
		var lost atomic.Int32
		m := newLeaseManager(q, uuid.NullUUID{UUID: uuid.New(), Valid: true}, 5*time.Second, time.Second, 1, func(Task, error) { lost.Add(1) })
		var stops []func()
		for id := int32(1); id <= 600; id++ {
			stops = append(stops, m.register(context.Background(), Task{ID: id, LeaseVersion: 9}))
		}
		time.Sleep(12 * time.Second) // Multiple TTLs; successful batches keep every lease alive.
		synctest.Wait()
		require.Zero(t, lost.Load())
		require.Equal(t, int32(1), peak.Load())
		require.Greater(t, calls.Load(), int32(10))
		for _, stop := range stops {
			stop()
		}
		synctest.Wait()
		m.mu.Lock()
		require.Empty(t, m.entries)
		require.False(t, m.running)
		m.mu.Unlock()
	})
}

func TestLeaseDeadlineInterruptsWhileQueryIsBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})
		q := leaseTestQueries{refresh: func(ctx context.Context, p querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error) {
			close(started)
			<-release // Deliberately ignore cancellation to test the scheduler separately.
			return renewedRows(p), nil
		}}
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		m := newLeaseManager(q, uuid.NullUUID{}, 2*time.Second, time.Second, 1, func(_ Task, err error) { cancel(err) })
		stop := m.register(ctx, Task{ID: 1, LeaseVersion: 1})
		<-started
		<-ctx.Done()
		require.ErrorIs(t, context.Cause(ctx), taskcore.ErrTaskLockLost)
		close(release)
		stop()
		synctest.Wait()
	})
}

func TestLeaseManagerCoalescesStaggeredDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var sizes []int
		q := leaseTestQueries{refresh: func(_ context.Context, p querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error) {
			sizes = append(sizes, len(p.Ids))
			time.Sleep(10 * time.Millisecond)
			return renewedRows(p), nil
		}}
		m := newLeaseManager(q, uuid.NullUUID{}, 5*time.Second, time.Second, 1, func(Task, error) { t.Error("unexpected lost lease") })
		var stops []func()
		for id := int32(0); id < 10; id++ {
			stops = append(stops, m.register(context.Background(), Task{ID: id}))
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(1400 * time.Millisecond)
		synctest.Wait()
		require.Equal(t, []int{1, 9}, sizes)
		for _, stop := range stops {
			stop()
		}
	})
}

func TestLeaseManagerHandlesPartialRenewalsIndependently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		q := leaseTestQueries{
			refresh: func(_ context.Context, p querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error) {
				calls.Add(1)
				return []*querier.RefreshTaskLocksRow{{ID: 1, LeaseVersion: 10}}, nil
			},
			status: func(_ context.Context, ids []int32) ([]*querier.ListTaskWaitStatusesRow, error) {
				require.ElementsMatch(t, []int32{2, 3, 4}, ids)
				return []*querier.ListTaskWaitStatusesRow{{ID: 2, Status: "paused"}, {ID: 3, Status: "cancelled"}, {ID: 4, Status: "running"}}, nil
			},
		}
		contexts := make([]context.Context, 4)
		cancels := make([]context.CancelCauseFunc, 4)
		for i := range contexts {
			contexts[i], cancels[i] = context.WithCancelCause(context.Background())
			defer cancels[i](nil)
		}
		m := newLeaseManager(q, uuid.NullUUID{}, 5*time.Second, time.Second, 1, func(task Task, err error) { cancels[task.ID-1](err) })
		var stops []func()
		for i := range contexts {
			stops = append(stops, m.register(contexts[i], Task{ID: int32(i + 1), LeaseVersion: 10}))
		}
		time.Sleep(time.Second)
		synctest.Wait()
		require.NoError(t, contexts[0].Err())
		require.ErrorIs(t, context.Cause(contexts[1]), taskcore.ErrTaskPaused)
		require.ErrorIs(t, context.Cause(contexts[2]), taskcore.ErrTaskCancelled)
		require.ErrorIs(t, context.Cause(contexts[3]), taskcore.ErrTaskLockLost)
		require.Equal(t, int32(1), calls.Load())
		for _, stop := range stops {
			stop()
		}
	})
}

func TestRemovingOneLeaseDrainsBatchWithoutCancellingOthers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		q := leaseTestQueries{refresh: func(ctx context.Context, p querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error) {
			close(started)
			select {
			case <-ctx.Done():
				t.Error("another task still needs this batch")
				return nil, ctx.Err()
			case <-release:
			}
			return renewedRows(p), nil
		}}
		m := newLeaseManager(q, uuid.NullUUID{}, 5*time.Second, time.Second, 1, func(Task, error) { t.Error("unexpected lost lease") })
		stop1 := m.register(context.Background(), Task{ID: 1})
		stop2 := m.register(context.Background(), Task{ID: 2})
		<-started
		stopped := make(chan struct{})
		go func() { stop1(); close(stopped) }()
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("finalization could overlap renewal")
		default:
		}
		close(release)
		<-stopped
		stop2()
	})
}

func TestRemovingLastLeaseCancelsBlockedQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		q := leaseTestQueries{refresh: func(ctx context.Context, p querier.RefreshTaskLocksParams) ([]*querier.RefreshTaskLocksRow, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		m := newLeaseManager(q, uuid.NullUUID{}, 5*time.Second, time.Second, 1, func(Task, error) { t.Error("unexpected lost lease") })
		stop := m.register(context.Background(), Task{ID: 1})
		<-started
		before := time.Now()
		stop()
		require.Equal(t, before, time.Now(), "removal should cancel, not wait for lease expiry")
		synctest.Wait()
	})
}
