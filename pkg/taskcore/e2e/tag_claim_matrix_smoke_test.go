//go:build smoke

package taskcoree2e_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/taskcore/ctrl"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func runTagClaimPathMatrix(t *testing.T, ctx context.Context, m model.ModelInterface) {
	t.Helper()
	for _, path := range []string{"strict", "normal", "normal_strict_fallback", "manual", "generic"} {
		for _, serial := range []bool{false, true} {
			name := path
			if serial {
				name += "_serial"
			}
			t.Run(name, func(t *testing.T) {
				require.NoError(t, resetDSTState(ctx, m))
				s := store.NewTaskStore(m)
				control := ctrl.NewWorkerControlPlane(m, nil, s, nil)
				for _, tag := range []string{"a", "b"} {
					require.NoError(t, control.SetTagConcurrencyLimit(ctx, tag, 1))
				}
				workerID := uuid.New()
				p, err := worker.NewModelPort(m, workerID, []string{"gpu"}, nil, 9*time.Second, 0)
				require.NoError(t, err)
				push := func(attrs apigen.TaskAttributes) int32 {
					id, err := s.PushTask(ctx, &apigen.Task{Status: apigen.Pending, Spec: apigen.TaskSpec{Type: "matrix", Payload: []byte("{}")}, Attributes: attrs})
					require.NoError(t, err)
					return id
				}
				holderID := push(apigen.TaskAttributes{Tags: regressionPtr([]string{"a"})})
				holder, err := p.ClaimByID(ctx, holderID, worker.ClaimRequest{})
				require.NoError(t, err)
				priority := int32(1)
				if path == "normal" {
					priority = 0
				}
				attrs := apigen.TaskAttributes{Tags: regressionPtr([]string{"b", "a"}), Labels: regressionPtr([]string{"gpu"}), Priority: &priority}
				if serial {
					attrs.SerialKey, attrs.SerialID = regressionPtr("serial"), regressionPtr(int32(1))
				}
				id := push(attrs)
				var nextID int32
				if serial {
					nextID = push(apigen.TaskAttributes{Tags: regressionPtr([]string{"b"}), SerialKey: attrs.SerialKey, SerialID: regressionPtr(int32(2))})
				}
				claim := func() (*worker.Task, error) {
					switch path {
					case "strict":
						return p.ClaimStrict(ctx, worker.ClaimRequest{})
					case "normal", "normal_strict_fallback":
						return p.ClaimNormalByGroup(ctx, worker.ClaimNormalRequest{Group: worker.DefaultWeightGroup, ClaimRequest: worker.ClaimRequest{AllowStrict: path == "normal_strict_fallback"}})
					case "manual":
						return p.ClaimByID(ctx, id, worker.ClaimRequest{AllowStrict: true})
					default:
						var out *querier.AnclaxTask
						err := m.RunTransactionWithTx(ctx, func(_ core.Tx, txm model.ModelInterface) error {
							var err error
							out, err = txm.ClaimTask(ctx, querier.ClaimTaskParams{WorkerID: uuid.NullUUID{UUID: workerID, Valid: true}, Labels: []string{"gpu"}, LockTtlMs: 9000})
							if errors.Is(err, pgx.ErrNoRows) {
								out = nil
								return nil
							}
							return err
						})
						if err != nil {
							return nil, err
						}
						if out == nil {
							return nil, worker.ErrNoTask
						}
						return &worker.Task{ID: out.ID, LeaseVersion: out.LeaseVersion, Attempts: out.Attempts, Attributes: out.Attributes, Spec: out.Spec}, nil
					}
				}
				_, err = claim()
				require.ErrorIs(t, err, worker.ErrNoTask)
				for tag, want := range map[string]int32{"a": 1, "b": 0} {
					usage, err := control.GetTagConcurrency(ctx, tag)
					require.NoError(t, err)
					require.Equal(t, want, usage.InUse)
				}
				blocked, err := m.GetTaskByID(ctx, id)
				require.NoError(t, err)
				require.Zero(t, blocked.Attempts)
				require.Nil(t, blocked.LockedAt)
				if serial {
					_, err = p.ClaimByID(ctx, nextID, worker.ClaimRequest{})
					require.ErrorIs(t, err, worker.ErrNoTask, "a tag-blocked serial head must not be overtaken")
				}
				require.NoError(t, p.FinalizeTask(ctx, *holder, nil))
				wrongLabels, err := worker.NewModelPort(m, uuid.New(), nil, nil, time.Second, 0)
				require.NoError(t, err)
				_, err = wrongLabels.ClaimByID(ctx, id, worker.ClaimRequest{AllowStrict: true})
				require.ErrorIs(t, err, worker.ErrNoTask)
				future := time.Now().Add(time.Hour)
				require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{ID: id, StartedAt: &future}))
				_, err = claim()
				require.ErrorIs(t, err, worker.ErrNoTask, "free permits must not bypass scheduling")
				require.NoError(t, m.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{ID: id}))
				task, err := claim()
				require.NoError(t, err)
				require.Equal(t, id, task.ID)
				if serial {
					_, err = p.ClaimByID(ctx, nextID, worker.ClaimRequest{})
					require.ErrorIs(t, err, worker.ErrNoTask)
				}
				require.NoError(t, p.FinalizeTask(ctx, *task, nil))
				if serial {
					next, err := p.ClaimByID(ctx, nextID, worker.ClaimRequest{})
					require.NoError(t, err)
					require.NoError(t, p.FinalizeTask(ctx, *next, nil))
				}
				for _, tag := range []string{"a", "b"} {
					usage, err := control.GetTagConcurrency(ctx, tag)
					require.NoError(t, err)
					require.Zero(t, usage.InUse)
				}
			})
		}
	}
}
