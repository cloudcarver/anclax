package worker

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestClaimMaintenanceFailurePreventsAdmission(t *testing.T) {
	for name, claim := range map[string]func(context.Context, *ModelPort) error{
		"strict": func(ctx context.Context, p *ModelPort) error {
			_, err := p.ClaimStrict(ctx, ClaimRequest{})
			return err
		},
		"normal": func(ctx context.Context, p *ModelPort) error {
			_, err := p.ClaimNormalByGroup(ctx, ClaimNormalRequest{})
			return err
		},
		"manual": func(ctx context.Context, p *ModelPort) error {
			_, err := p.ClaimByID(ctx, 7, ClaimRequest{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, failure := range []error{errors.New("database unavailable"), context.Canceled} {
				m := model.NewMockModelInterface(gomock.NewController(t))
				p, err := NewModelPort(m, uuid.New(), nil, nil, time.Second, 0)
				require.NoError(t, err)
				m.EXPECT().MaintainTaskConcurrency(gomock.Any(), int64(1000)).Return(failure)
				// No transaction/claim expectation: admitting work on this path fails.
				require.ErrorIs(t, claim(context.Background(), p), failure)
			}
		})
	}
}

func TestConcurrencyMaintenanceCoalescesRetriesAndResumesAfterCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := model.NewMockModelInterface(gomock.NewController(t))
		p, err := NewModelPort(m, uuid.New(), nil, nil, time.Second, 0)
		require.NoError(t, err)
		ctx := context.Background()
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		failure := errors.New("temporary maintenance failure")
		gomock.InOrder(
			m.EXPECT().MaintainTaskConcurrency(ctx, int64(1000)).DoAndReturn(func(context.Context, int64) error {
				close(entered)
				<-release
				return failure
			}),
			m.EXPECT().MaintainTaskConcurrency(ctx, int64(1000)).Return(nil),
			m.EXPECT().MaintainTaskConcurrency(ctx, int64(1000)).Return(nil),
		)
		go func() { done <- p.maintainConcurrency(ctx) }()
		<-entered
		require.NoError(t, p.maintainConcurrency(ctx), "a concurrent sweep must not wait for a slow database")
		close(release)
		require.ErrorIs(t, <-done, failure)
		require.NoError(t, p.maintainConcurrency(ctx), "failed maintenance must be immediately retryable")
		require.NoError(t, p.maintainConcurrency(ctx), "successful maintenance is shared during cooldown")
		time.Sleep(250 * time.Millisecond)
		require.NoError(t, p.maintainConcurrency(ctx), "maintenance must resume after cooldown")
	})
}

func TestBlockedBusinessClaimCommitsAndCommitFailureIsReturned(t *testing.T) {
	for _, name := range []string{"strict", "normal", "manual"} {
		t.Run(name, func(t *testing.T) {
			for _, commitErr := range []error{nil, errors.New("commit connection lost")} {
				mock := gomock.NewController(t)
				m, txm := model.NewMockModelInterface(mock), model.NewMockModelInterfaceWithTransaction(mock)
				p, err := NewModelPort(m, uuid.New(), nil, nil, time.Second, 0)
				require.NoError(t, err)
				m.EXPECT().MaintainTaskConcurrency(gomock.Any(), int64(1000)).Return(nil)
				m.EXPECT().RunTransactionWithTx(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error {
					// ErrNoRows must commit the SQL function's wait marker, not roll it back.
					require.NoError(t, f(nil, txm))
					return commitErr
				})
				var task *Task
				// sqlc can return an allocated zero row together with ErrNoRows.
				missingRow := &querier.AnclaxTask{}
				switch name {
				case "strict":
					txm.EXPECT().ClaimStrictTask(gomock.Any(), gomock.Any()).Return(missingRow, pgx.ErrNoRows)
					task, err = p.ClaimStrict(context.Background(), ClaimRequest{})
				case "normal":
					txm.EXPECT().ClaimNormalTaskByGroup(gomock.Any(), gomock.Any()).Return(missingRow, pgx.ErrNoRows)
					task, err = p.ClaimNormalByGroup(context.Background(), ClaimNormalRequest{})
				case "manual":
					txm.EXPECT().ClaimTaskByID(gomock.Any(), querier.ClaimTaskByIDParams{ID: 7, WorkerID: p.workerIDParam, LockTtlMs: 1000}).Return(missingRow, pgx.ErrNoRows)
					task, err = p.ClaimByID(context.Background(), 7, ClaimRequest{})
				}
				require.Nil(t, task)
				if commitErr != nil {
					require.ErrorIs(t, err, commitErr)
				} else {
					require.ErrorIs(t, err, ErrNoTask)
				}
			}
		})
	}
}

func TestSystemClaimDoesNotDependOnBusinessMaintenance(t *testing.T) {
	ctrl := gomock.NewController(t)
	m, txm := model.NewMockModelInterface(ctrl), model.NewMockModelInterfaceWithTransaction(ctrl)
	p, err := NewModelPort(m, uuid.New(), nil, nil, time.Second, 0)
	require.NoError(t, err)
	m.EXPECT().RunTransactionWithTx(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, f func(core.Tx, model.ModelInterface) error) error { return f(nil, txm) })
	txm.EXPECT().ClaimWorkerCommand(gomock.Any(), gomock.Any()).Return(&querier.AnclaxTask{ID: 7}, nil)
	task, err := p.ClaimControl(context.Background(), ClaimRequest{})
	require.NoError(t, err)
	require.Equal(t, int32(7), task.ID)
}
