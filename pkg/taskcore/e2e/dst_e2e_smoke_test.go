//go:build smoke
// +build smoke

package taskcoree2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	taskcoree2e "github.com/cloudcarver/anclax/pkg/taskcore/e2e/gen"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/stretchr/testify/require"
)

// TestDSTTaskStoreScenariosSmoke is the primary DST smoke entrypoint for taskcore.
func TestDSTTaskStoreScenariosSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		env, err := newDSTEnv(m)
		require.NoError(t, err)

		err = taskcoree2e.RunAll(ctx, func(ctx context.Context) (taskcoree2e.Actors, error) {
			return taskcoree2e.Actors{
				TaskStore:    env.taskStore,
				Runtime:      env.runtime,
				Validator:    env.validator,
				ControlPlane: env.controlPlane,
			}, nil
		})
		require.NoError(t, err)
	})
}

// TestDSTTaskStoreScenariosStressSmoke repeats the same DST suite for stress checks.
func TestDSTTaskStoreScenariosStressSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		var prevEnv *dstEnv
		report, err := taskcoree2e.RunAllWithReport(ctx, func(ctx context.Context) (taskcoree2e.Actors, error) {
			if prevEnv != nil {
				if err := prevEnv.runtime.stopAllWorkers(ctx, 3*time.Second); err != nil {
					return taskcoree2e.Actors{}, err
				}
			}
			if err := resetDSTState(ctx, m); err != nil {
				return taskcoree2e.Actors{}, err
			}
			env, err := newDSTEnv(m)
			if err != nil {
				return taskcoree2e.Actors{}, err
			}
			prevEnv = env
			return taskcoree2e.Actors{
				TaskStore:    env.taskStore,
				Runtime:      env.runtime,
				Validator:    env.validator,
				ControlPlane: env.controlPlane,
			}, nil
		}, taskcoree2e.RunOptions{Repeat: 3, ContinueOnError: true})
		if prevEnv != nil {
			require.NoError(t, prevEnv.runtime.stopAllWorkers(ctx, 3*time.Second))
		}
		for _, run := range report.Runs {
			t.Logf("stress run %d duration=%s err=%v", run.Iteration, run.Duration, run.Err)
		}
		require.NoError(t, err)
		require.Equal(t, 0, report.FailedRuns)
	})
}

type dstEnv struct {
	store        taskcore.TaskStoreInterface
	taskStore    *taskStoreActor
	runtime      *runtimeActor
	validator    *validatorActor
	controlPlane *controlPlaneActor
}

func newDSTEnv(m model.ModelInterface) (*dstEnv, error) {
	store := taskcore.NewTaskStore(m)

	taskStore := &taskStoreActor{
		model: m,
		store: store,
	}

	return &dstEnv{
		store:        store,
		taskStore:    taskStore,
		runtime:      newRuntimeActor(m),
		validator:    newValidatorActor(m),
		controlPlane: newControlPlaneActor(m, store),
	}, nil
}

func resetDSTState(ctx context.Context, m model.ModelInterface) error {
	return m.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		_, err := tx.Exec(ctx, `
			TRUNCATE TABLE
				anclax.events,
				anclax.tasks,
				anclax.workers,
				anclax.worker_runtime_configs
			RESTART IDENTITY CASCADE
		`)
		return err
	})
}

type taskStoreActor struct {
	model model.ModelInterface
	store taskcore.TaskStoreInterface
}

type validatorActor struct {
	model model.ModelInterface
}

func newValidatorActor(model model.ModelInterface) *validatorActor {
	return &validatorActor{model: model}
}

func (v *validatorActor) Query(ctx context.Context, sql string, args []any) ([][]any, error) {
	var rows [][]any
	err := v.model.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		r, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			vals, err := r.Values()
			if err != nil {
				return err
			}
			rows = append(rows, vals)
		}
		return r.Err()
	})
	return rows, err
}

func (a *taskStoreActor) Enqueue(ctx context.Context, task string, priority int32, weight int32, labels []string) error {
	if task == "" {
		return fmt.Errorf("task name is required")
	}
	if priority < 0 {
		return fmt.Errorf("priority must be non-negative")
	}
	if weight < 1 {
		return fmt.Errorf("weight must be >= 1")
	}

	payload, err := json.Marshal(map[string]string{"name": task})
	if err != nil {
		return err
	}

	attrs := apigen.TaskAttributes{
		Priority: int32Ptr(priority),
		Weight:   int32Ptr(weight),
	}
	if len(labels) > 0 {
		labelsCopy := append([]string(nil), labels...)
		attrs.Labels = &labelsCopy
	}

	_, err = a.store.PushTask(ctx, &apigen.Task{
		Attributes: attrs,
		Spec: apigen.TaskSpec{
			Type:    "dst-taskstore",
			Payload: payload,
		},
		Status: apigen.Pending,
	})
	if err != nil {
		return err
	}
	return nil
}

func (a *taskStoreActor) EnqueueRaw(
	ctx context.Context,
	task string,
	taskType string,
	payload string,
	priority int32,
	weight int32,
	labels []string,
	retryInterval string,
	retryMaxAttempts int32,
	cronExpression string,
	startInSeconds int32,
) error {
	if task == "" || taskType == "" {
		return fmt.Errorf("task name and type are required")
	}
	if priority < 0 {
		return fmt.Errorf("priority must be non-negative")
	}
	if weight < 1 {
		return fmt.Errorf("weight must be >= 1")
	}
	if startInSeconds < -86400 {
		return fmt.Errorf("startInSeconds too small")
	}

	rawPayload := payload
	if rawPayload == "" {
		rawPayload = "{}"
	}
	payloadObj := map[string]any{}
	if err := json.Unmarshal([]byte(rawPayload), &payloadObj); err != nil {
		return fmt.Errorf("payload must be a JSON object: %w", err)
	}
	payloadObj["name"] = task
	payloadBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return err
	}

	attrs := apigen.TaskAttributes{
		Priority: int32Ptr(priority),
		Weight:   int32Ptr(weight),
	}
	if len(labels) > 0 {
		labelsCopy := append([]string(nil), labels...)
		attrs.Labels = &labelsCopy
	}
	if retryInterval != "" || retryMaxAttempts != 0 {
		attrs.RetryPolicy = &apigen.TaskRetryPolicy{
			Interval:    retryInterval,
			MaxAttempts: retryMaxAttempts,
		}
	}
	if cronExpression != "" {
		attrs.Cronjob = &apigen.TaskCronjob{
			CronExpression: cronExpression,
		}
	}

	var startedAt *time.Time
	if startInSeconds != 0 {
		v := time.Now().Add(time.Duration(startInSeconds) * time.Second)
		startedAt = &v
	}

	_, err = a.store.PushTask(ctx, &apigen.Task{
		Attributes: attrs,
		Spec: apigen.TaskSpec{
			Type:    taskType,
			Payload: payloadBytes,
		},
		Status:    apigen.Pending,
		StartedAt: startedAt,
	})
	if err != nil {
		return err
	}
	return nil
}

func (a *taskStoreActor) EnqueueSerial(ctx context.Context, task string, serialKey string, serialID int32, startInSeconds int32) error {
	if task == "" || serialKey == "" {
		return fmt.Errorf("task and serialKey are required")
	}
	if serialID < 0 {
		return fmt.Errorf("serialID must be non-negative")
	}

	payload, err := json.Marshal(map[string]string{"name": task})
	if err != nil {
		return err
	}

	startAt := time.Now().Add(time.Duration(startInSeconds) * time.Second)
	_, err = a.store.PushTask(ctx, &apigen.Task{
		Attributes: apigen.TaskAttributes{
			SerialKey: &serialKey,
			SerialID:  int32Ptr(serialID),
		},
		Spec: apigen.TaskSpec{
			Type:    "dst-taskstore",
			Payload: payload,
		},
		Status:    apigen.Pending,
		StartedAt: &startAt,
	})
	if err != nil {
		return err
	}
	return nil
}

func (a *taskStoreActor) SetTaskStartOffset(ctx context.Context, task string, offsetSeconds int32) error {
	id, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	startedAt := time.Now().Add(time.Duration(offsetSeconds) * time.Second)
	return a.model.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{
		ID:        id,
		StartedAt: &startedAt,
	})
}

func (a *taskStoreActor) SetTaskParent(ctx context.Context, task string, parent string) error {
	if task == "" || parent == "" {
		return fmt.Errorf("task and parent are required")
	}
	childID, err := a.taskIDByName(ctx, task)
	if err != nil {
		return err
	}
	parentID, err := a.taskIDByName(ctx, parent)
	if err != nil {
		return err
	}
	return a.model.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		_, err := tx.Exec(ctx, "update anclax.tasks set parent_task_id = $1 where id = $2", parentID, childID)
		return err
	})
}

func (a *taskStoreActor) taskIDByName(ctx context.Context, task string) (int32, error) {
	var id int32
	err := a.model.RunTransactionWithTx(ctx, func(tx core.Tx, _ model.ModelInterface) error {
		return tx.QueryRow(ctx, "select id from anclax.tasks where spec->'payload'->>'name' = $1 order by created_at desc limit 1", task).Scan(&id)
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (a *taskStoreActor) Sleep(ctx context.Context, seconds int32) error {
	if seconds < 0 {
		return fmt.Errorf("sleep seconds must be non-negative")
	}
	t := time.NewTimer(time.Duration(seconds) * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func int32Ptr(v int32) *int32 { return &v }
