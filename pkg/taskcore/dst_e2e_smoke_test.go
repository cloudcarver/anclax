//go:build smoke
// +build smoke

package taskcore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore"
	taskcoree2e "github.com/cloudcarver/anclax/pkg/taskcore/e2e/gen"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestDSTTaskStoreScenariosSmoke(t *testing.T) {
	withSmokePostgres(t, func(ctx context.Context, m model.ModelInterface) {
		env, err := newDSTEnv(ctx, m)
		require.NoError(t, err)

		err = taskcoree2e.RunAll(ctx, func(ctx context.Context) (taskcoree2e.Actors, error) {
			return taskcoree2e.Actors{
				TaskStore: env.taskStore,
				Worker1:   env.worker1,
				Worker2:   env.worker2,
			}, nil
		})
		require.NoError(t, err)
	})
}

type dstEnv struct {
	store     taskcore.TaskStoreInterface
	taskStore *taskStoreActor
	worker1   *workerActor
	worker2   *workerActor
}

func newDSTEnv(ctx context.Context, m model.ModelInterface) (*dstEnv, error) {
	store := taskcore.NewTaskStore(m)
	tasks := newTaskRegistry()
	claims := newClaimTracker()

	taskStore := &taskStoreActor{
		model: m,
		store: store,
		tasks: tasks,
	}

	worker1, err := newWorkerActor(ctx, m, "worker1", nil, tasks, claims)
	if err != nil {
		return nil, err
	}
	worker2, err := newWorkerActor(ctx, m, "worker2", nil, tasks, claims)
	if err != nil {
		return nil, err
	}

	return &dstEnv{
		store:     store,
		taskStore: taskStore,
		worker1:   worker1,
		worker2:   worker2,
	}, nil
}

type taskRegistry struct {
	mu       sync.RWMutex
	nameToID map[string]int32
	idToName map[int32]string
}

func newTaskRegistry() *taskRegistry {
	return &taskRegistry{
		nameToID: map[string]int32{},
		idToName: map[int32]string{},
	}
}

func (r *taskRegistry) put(name string, id int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nameToID[name] = id
	r.idToName[id] = name
}

func (r *taskRegistry) id(name string) (int32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.nameToID[name]
	return id, ok
}

func (r *taskRegistry) name(id int32) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name, ok := r.idToName[id]
	return name, ok
}

type claimTracker struct {
	mu     sync.Mutex
	active map[int32]string
}

func newClaimTracker() *claimTracker {
	return &claimTracker{active: map[int32]string{}}
}

func (c *claimTracker) acquire(taskID int32, worker string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner, ok := c.active[taskID]; ok {
		return fmt.Errorf("task %d already actively claimed by %s", taskID, owner)
	}
	c.active[taskID] = worker
	return nil
}

func (c *claimTracker) release(taskID int32, worker string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	owner, ok := c.active[taskID]
	if !ok {
		return fmt.Errorf("task %d is not actively tracked", taskID)
	}
	if owner != worker {
		return fmt.Errorf("task %d tracked owner mismatch: have=%s release=%s", taskID, owner, worker)
	}
	delete(c.active, taskID)
	return nil
}

type taskStoreActor struct {
	model model.ModelInterface
	store taskcore.TaskStoreInterface
	tasks *taskRegistry
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

	id, err := a.store.PushTask(ctx, &apigen.Task{
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
	a.tasks.put(task, id)
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
	id, err := a.store.PushTask(ctx, &apigen.Task{
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
	a.tasks.put(task, id)
	return nil
}

func (a *taskStoreActor) SetTaskStartOffset(ctx context.Context, task string, offsetSeconds int32) error {
	id, ok := a.tasks.id(task)
	if !ok {
		return fmt.Errorf("unknown task %q", task)
	}
	startedAt := time.Now().Add(time.Duration(offsetSeconds) * time.Second)
	return a.model.UpdateTaskStartedAt(ctx, querier.UpdateTaskStartedAtParams{
		ID:        id,
		StartedAt: &startedAt,
	})
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

func (a *taskStoreActor) ExpectStatus(ctx context.Context, task string, status string) error {
	id, ok := a.tasks.id(task)
	if !ok {
		return fmt.Errorf("unknown task %q", task)
	}
	qt, err := a.model.GetTaskByID(ctx, id)
	if err != nil {
		return err
	}
	if qt.Status != status {
		return fmt.Errorf("unexpected task status for %s: got=%s want=%s", task, qt.Status, status)
	}
	return nil
}

func (a *taskStoreActor) ExpectNoPending(ctx context.Context) error {
	pending, err := a.model.ListAllPendingTasks(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	names := make([]string, 0, len(pending))
	for _, t := range pending {
		if name, ok := a.tasks.name(t.ID); ok {
			names = append(names, name)
		} else {
			names = append(names, fmt.Sprintf("id=%d", t.ID))
		}
	}
	return fmt.Errorf("expected no pending tasks, still pending: %v", names)
}

type workerActor struct {
	name      string
	model     model.ModelInterface
	workerID  uuid.NullUUID
	labels    []string
	hasLabels bool
	tasks     *taskRegistry
	claims    *claimTracker

	mu            sync.Mutex
	lastClaimID   int32
	lastClaimName string
}

func newWorkerActor(ctx context.Context, m model.ModelInterface, name string, labels []string, tasks *taskRegistry, claims *claimTracker) (*workerActor, error) {
	id := uuid.New()
	labelJSON, err := json.Marshal(labels)
	if err != nil {
		return nil, err
	}
	_, err = m.UpsertWorker(ctx, querier.UpsertWorkerParams{
		ID:                   id,
		Labels:               labelJSON,
		AppliedConfigVersion: 0,
	})
	if err != nil {
		return nil, err
	}

	return &workerActor{
		name:      name,
		model:     m,
		workerID:  uuid.NullUUID{UUID: id, Valid: true},
		labels:    append([]string(nil), labels...),
		hasLabels: len(labels) > 0,
		tasks:     tasks,
		claims:    claims,
	}, nil
}

func (w *workerActor) SetLabels(ctx context.Context, labels []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.labels = append([]string(nil), labels...)
	w.hasLabels = len(w.labels) > 0
	return nil
}

func (w *workerActor) Claim(ctx context.Context) error {
	return w.ClaimWithLockTTL(ctx, 60)
}

func (w *workerActor) ClaimWithLockTTL(ctx context.Context, lockTTLSeconds int32) error {
	if lockTTLSeconds < 0 {
		return fmt.Errorf("lockTTLSeconds must be non-negative")
	}
	lockExpiry := time.Now().Add(-time.Duration(lockTTLSeconds) * time.Second)
	qt, err := w.model.ClaimTask(ctx, querier.ClaimTaskParams{
		WorkerID:   w.workerID,
		LockExpiry: &lockExpiry,
		HasLabels:  w.hasLabels,
		Labels:     w.labels,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			w.clearLastClaim()
			return nil
		}
		return err
	}
	return w.setClaimedTask(qt.ID)
}

func (w *workerActor) ClaimStrict(ctx context.Context) error {
	lockExpiry := time.Now().Add(-1 * time.Minute)
	qt, err := w.model.ClaimStrictTask(ctx, querier.ClaimStrictTaskParams{
		WorkerID:   w.workerID,
		LockExpiry: &lockExpiry,
		HasLabels:  w.hasLabels,
		Labels:     w.labels,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			w.clearLastClaim()
			return nil
		}
		return err
	}
	return w.setClaimedTask(qt.ID)
}

func (w *workerActor) ClaimGroup(ctx context.Context, group string, weightedLabels []string) error {
	lockExpiry := time.Now().Add(-1 * time.Minute)
	qt, err := w.model.ClaimNormalTaskByGroup(ctx, querier.ClaimNormalTaskByGroupParams{
		WorkerID:       w.workerID,
		LockExpiry:     &lockExpiry,
		HasLabels:      w.hasLabels,
		Labels:         w.labels,
		GroupName:      group,
		WeightedLabels: weightedLabels,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			w.clearLastClaim()
			return nil
		}
		return err
	}
	return w.setClaimedTask(qt.ID)
}

func (w *workerActor) CompleteLast(ctx context.Context) error {
	id, _, ok := w.lastClaim()
	if !ok {
		return fmt.Errorf("%s has no claimed task", w.name)
	}
	_, err := w.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       id,
		Status:   string(apigen.Completed),
		WorkerID: w.workerID,
	})
	if err != nil {
		return err
	}
	if err := w.claims.release(id, w.name); err != nil {
		return err
	}
	w.clearLastClaim()
	return nil
}

func (w *workerActor) FailLast(ctx context.Context) error {
	id, _, ok := w.lastClaim()
	if !ok {
		return fmt.Errorf("%s has no claimed task", w.name)
	}
	_, err := w.model.UpdateTaskStatusByWorker(ctx, querier.UpdateTaskStatusByWorkerParams{
		ID:       id,
		Status:   string(apigen.Failed),
		WorkerID: w.workerID,
	})
	if err != nil {
		return err
	}
	if err := w.claims.release(id, w.name); err != nil {
		return err
	}
	w.clearLastClaim()
	return nil
}

func (w *workerActor) AbandonLast(ctx context.Context) error {
	id, _, ok := w.lastClaim()
	if !ok {
		return nil
	}
	if err := w.claims.release(id, w.name); err != nil {
		return err
	}
	w.clearLastClaim()
	return nil
}

func (w *workerActor) ExpectLast(ctx context.Context, task string) error {
	_, name, ok := w.lastClaim()
	if !ok {
		return fmt.Errorf("%s expected claimed task %q but had none", w.name, task)
	}
	if name != task {
		return fmt.Errorf("%s expected claimed task %q, got %q", w.name, task, name)
	}
	return nil
}

func (w *workerActor) ExpectLastIn(ctx context.Context, tasks []string) error {
	_, name, ok := w.lastClaim()
	if !ok {
		return fmt.Errorf("%s expected claimed task in %v but had none", w.name, tasks)
	}
	for _, t := range tasks {
		if t == name {
			return nil
		}
	}
	return fmt.Errorf("%s expected claimed task in %v, got %q", w.name, tasks, name)
}

func (w *workerActor) ExpectNoClaim(ctx context.Context) error {
	_, name, ok := w.lastClaim()
	if ok {
		return fmt.Errorf("%s expected no claim, got %q", w.name, name)
	}
	return nil
}

func (w *workerActor) setClaimedTask(taskID int32) error {
	if err := w.claims.acquire(taskID, w.name); err != nil {
		return err
	}
	name, ok := w.tasks.name(taskID)
	if !ok {
		_ = w.claims.release(taskID, w.name)
		return fmt.Errorf("%s claimed unknown task id %d", w.name, taskID)
	}
	w.mu.Lock()
	w.lastClaimID = taskID
	w.lastClaimName = name
	w.mu.Unlock()
	return nil
}

func (w *workerActor) clearLastClaim() {
	w.mu.Lock()
	w.lastClaimID = 0
	w.lastClaimName = ""
	w.mu.Unlock()
}

func (w *workerActor) lastClaim() (int32, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastClaimID == 0 {
		return 0, "", false
	}
	return w.lastClaimID, w.lastClaimName, true
}

func int32Ptr(v int32) *int32 { return &v }
