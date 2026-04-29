//go:build smoke
// +build smoke

package chaos

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type chaosWorkerSlot struct {
	name      string
	labels    []string
	active    bool
	retired   bool
	restartAt int
}

type chaosTaskSlot struct {
	taskID          int32
	uniqueTag       string
	controlEligible bool
	controlMode     string
	finalStatus     string
	controlState    string
	resumeAt        int
	cancelAt        time.Time
}

type chaosState struct {
	rng                   *rand.Rand
	workers               []*chaosWorkerSlot
	tasks                 map[string]*chaosTaskSlot
	nextReplacement       int
	controlPlaneDownUntil int
	tasksSubmitted        int
	workerDisruptions     int
	postgresRestarts      int
	controlPlaneOutages   int
	runtimeConfigUpdates  int
	replacementWorkers    int
	userPauses            int
	userResumes           int
	userCancels           int
	componentDowns        map[string]int
	componentRestarts     map[string]int
}

func TestContainerizedTaskcoreChaosSmoke(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	cfg := DefaultRunConfig()
	cfg.Seed = readChaosInt64Env(t, "ANCLAX_TASKCORE_CHAOS_SEED", 424242)
	cfg.KeepArtifacts = true

	h, err := NewHarness(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	var runErr error
	var state *chaosState
	defer func() {
		if summary, err := buildSmokeSummary(ctx, h, state); err == nil {
			h.Report().SetSummary(summary)
			if summary != nil {
				t.Logf("chaos summary: tasks submitted=%d completed=%d cancelled=%d retried=%d downs=%v restarts=%v", summary.Tasks.Submitted, summary.Tasks.Completed, summary.Tasks.Cancelled, summary.Tasks.Retried, summary.Components.DownCounts, summary.Components.RestartCounts)
			}
		} else {
			h.Report().AddEvent("summary.error", "report", err.Error(), nil)
		}
		if runErr != nil {
			_ = h.CollectDiagnostics(ctx, runErr)
		}
		_ = h.Close(ctx)
		t.Logf("chaos artifacts: %s", h.ArtifactDir())
	}()

	must := func(err error) {
		if err != nil {
			runErr = err
			t.Fatal(err)
		}
	}

	must(h.Start(ctx))
	state = &chaosState{
		rng: rand.New(rand.NewSource(cfg.Seed)),
		workers: []*chaosWorkerSlot{
			{name: "worker-a", labels: []string{"w1"}},
			{name: "worker-b", labels: []string{"w2"}},
			{name: "worker-c", labels: []string{"w1", "w2"}},
			{name: "worker-fair", labels: []string{"retry-fairness"}},
		},
		tasks:             map[string]*chaosTaskSlot{},
		componentDowns:    map[string]int{},
		componentRestarts: map[string]int{},
	}
	for _, slot := range state.workers {
		must(h.StartWorker(ctx, slot.name, slot.labels))
		slot.active = true
		must(h.WaitWorkerOnline(ctx, slot.name, true, 20*time.Second))
	}

	user := h.User()
	must(runInitialUserCancel(ctx, user, state))
	must(runInitialUserPauseResume(ctx, user, state))
	must(runInitialUserTagControl(ctx, user, state))
	must(runInitialInfiniteRetryFairness(ctx, user, state))
	iterations := readChaosPositiveEnvInt(t, "ANCLAX_TASKCORE_CHAOS_ITERATIONS", 28)
	interIterSleep := time.Duration(readChaosPositiveEnvInt(t, "ANCLAX_TASKCORE_CHAOS_INTER_ITER_SLEEP_MS", 300)) * time.Millisecond
	for iter := 1; iter <= iterations; iter++ {
		must(reconcileChaosState(ctx, h, state, iter))
		if state.controlPlaneAvailable(iter) {
			must(submitChaosBatch(ctx, user, state, iter))
			must(runUserOperation(ctx, user, state, iter))
		}
		must(runRandomAction(ctx, h, state, iter))
		time.Sleep(interIterSleep)
	}

	must(restoreCluster(ctx, h, state, iterations+1))
	must(resumeRemainingPausedTasks(ctx, user, state, iterations+1))
	for _, task := range state.tasks {
		switch task.finalStatus {
		case "cancelled":
			must(user.ExpectCancelled(ctx, task.uniqueTag, 60*time.Second))
		default:
			must(user.ExpectCompleted(ctx, task.uniqueTag, 60*time.Second))
		}
	}

	pending, err := h.Inspector().CountTasksByStatuses(ctx, []string{"pending", "running"}, "LONG-")
	must(err)
	require.Equal(t, int64(0), pending)
	if state.workerDisruptions > 0 || state.postgresRestarts > 0 {
		retried, err := h.Inspector().CountRetriedTasks(ctx, "LONG-")
		must(err)
		require.Greater(t, retried, int64(0))
	}
	require.Greater(t, len(state.tasks), 0)
	require.Greater(t, state.userPauses, 0)
	require.Greater(t, state.userResumes, 0)
	require.Greater(t, state.userCancels, 0)
}

func buildSmokeSummary(ctx context.Context, h *Harness, state *chaosState) (*ReportSummary, error) {
	if h == nil || h.Inspector() == nil || state == nil {
		return nil, nil
	}
	inspector := h.Inspector()
	observed, err := inspector.CountTasks(ctx, "LONG-")
	if err != nil {
		return nil, err
	}
	completed, err := inspector.CountTasksByStatuses(ctx, []string{"completed"}, "LONG-")
	if err != nil {
		return nil, err
	}
	pending, err := inspector.CountTasksByStatuses(ctx, []string{"pending"}, "LONG-")
	if err != nil {
		return nil, err
	}
	running, err := inspector.CountTasksByStatuses(ctx, []string{"running"}, "LONG-")
	if err != nil {
		return nil, err
	}
	failed, err := inspector.CountTasksByStatuses(ctx, []string{"failed"}, "LONG-")
	if err != nil {
		return nil, err
	}
	cancelled, err := inspector.CountTasksByStatuses(ctx, []string{"cancelled"}, "LONG-")
	if err != nil {
		return nil, err
	}
	paused, err := inspector.CountTasksByStatuses(ctx, []string{"paused"}, "LONG-")
	if err != nil {
		return nil, err
	}
	retried, err := inspector.CountRetriedTasks(ctx, "LONG-")
	if err != nil {
		return nil, err
	}
	retiredWorkers := 0
	for _, slot := range state.workers {
		if slot != nil && slot.retired {
			retiredWorkers++
		}
	}
	processed := completed + failed + cancelled + paused
	return &ReportSummary{
		Components: ComponentSummary{
			DownCounts:    cloneIntMap(state.componentDowns),
			RestartCounts: cloneIntMap(state.componentRestarts),
		},
		Tasks: TaskSummary{
			Submitted: state.tasksSubmitted,
			Expected:  len(state.tasks),
			Observed:  observed,
			Processed: processed,
			Completed: completed,
			Pending:   pending,
			Running:   running,
			Failed:    failed,
			Cancelled: cancelled,
			Paused:    paused,
			Retried:   retried,
		},
		Scenario: ScenarioSummary{
			WorkerDisruptions:    state.workerDisruptions,
			PostgresRestarts:     state.postgresRestarts,
			ControlPlaneOutages:  state.controlPlaneOutages,
			RuntimeConfigUpdates: state.runtimeConfigUpdates,
			ReplacementWorkers:   state.replacementWorkers,
			UserPauses:           state.userPauses,
			UserResumes:          state.userResumes,
			UserCancels:          state.userCancels,
			ActiveWorkers:        state.activeWorkers(),
			RetiredWorkers:       retiredWorkers,
		},
	}, nil
}

func cloneIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func runInitialUserCancel(ctx context.Context, user *User, state *chaosState) error {
	if user == nil {
		return fmt.Errorf("user is nil")
	}
	labels, group := taskLabelsAndGroup(0)
	signalIntervalMs := int32(readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_SIGNAL_INTERVAL_MS", 200))
	uniqueTag := "LONG-000-cancel"
	taskID, err := user.SubmitCancelObservableProbe(ctx, SubmitCancelObservableProbeRequest{
		TaskName:         uniqueTag,
		Group:            group,
		Labels:           labels,
		SignalBaseURL:    user.SignalBaseURL,
		SignalIntervalMs: signalIntervalMs,
	})
	if err != nil {
		return err
	}
	task := &chaosTaskSlot{taskID: taskID, uniqueTag: uniqueTag, controlEligible: true, controlMode: "cancel", finalStatus: "completed"}
	state.tasks[uniqueTag] = task
	state.tasksSubmitted++
	ready, snapshot, err := waitForRunningSignals(ctx, user, task, 10*time.Second)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("initial cancel task %d did not start signaling", taskID)
	}
	if user.Report != nil {
		user.Report.AddEvent("user.expectation", fmt.Sprintf("%d", task.taskID), "signal threshold reached", map[string]any{"taskID": task.taskID, "count": snapshot.Count})
	}
	if err := user.CancelTask(ctx, task.uniqueTag); err != nil {
		return err
	}
	if err := user.ExpectCancelled(ctx, task.uniqueTag, 20*time.Second); err != nil {
		return err
	}
	if err := user.ExpectSignalsStopped(ctx, task.taskID, 750*time.Millisecond); err != nil {
		return err
	}
	task.finalStatus = "cancelled"
	task.controlState = "cancelled"
	state.userCancels++
	return nil
}

func runInitialUserPauseResume(ctx context.Context, user *User, state *chaosState) error {
	if user == nil || user.DB == nil {
		return fmt.Errorf("user is not ready")
	}
	labels, group := taskLabelsAndGroup(1)
	pauseDelayMs := int32(readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_CONTROL_DELAY_MS", 1500))
	pauseSleepMs := int32(readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_CONTROL_SLEEP_MS", 800))
	uniqueTag := "LONG-000-pause"
	taskID, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
		TaskName: uniqueTag,
		JobID:    91,
		SleepMs:  pauseSleepMs,
		DelayMs:  pauseDelayMs,
		Group:    group,
		Labels:   labels,
	})
	if err != nil {
		return err
	}
	task := &chaosTaskSlot{taskID: taskID, uniqueTag: uniqueTag, controlEligible: true, controlMode: "pause", finalStatus: "completed"}
	state.tasks[uniqueTag] = task
	state.tasksSubmitted++
	status, err := user.DB.TaskStatus(ctx, task.uniqueTag)
	if err != nil {
		return err
	}
	if status != "pending" {
		return fmt.Errorf("initial pause task %s status=%s want=pending", task.uniqueTag, status)
	}
	if err := user.PauseTask(ctx, task.uniqueTag); err != nil {
		return err
	}
	if err := user.ExpectPaused(ctx, task.uniqueTag, 20*time.Second); err != nil {
		return err
	}
	task.controlState = "paused"
	state.userPauses++
	if err := user.ResumeTask(ctx, task.uniqueTag); err != nil {
		return err
	}
	if err := user.ExpectPending(ctx, task.uniqueTag, 20*time.Second); err != nil {
		return err
	}
	task.controlState = "resumed"
	state.userResumes++
	return nil
}

func runInitialUserTagControl(ctx context.Context, user *User, state *chaosState) error {
	if user == nil || user.DB == nil {
		return fmt.Errorf("user is not ready")
	}
	commonTag := "chaos:tag-control"
	pauseTag := "action:pause"
	cancelTag := "action:cancel"
	exceptTag := "action:skip"
	type taggedTask struct {
		name        string
		jobID       int64
		labels      []string
		tags        []string
		finalStatus string
	}
	tasks := []taggedTask{
		{
			name:        "LONG-000-tags-pause-target",
			jobID:       7101,
			labels:      []string{"w1"},
			tags:        []string{commonTag, pauseTag, "org:chaos-a"},
			finalStatus: "completed",
		},
		{
			name:        "LONG-000-tags-pause-except",
			jobID:       7102,
			labels:      []string{"w2"},
			tags:        []string{commonTag, pauseTag, exceptTag, "org:chaos-a"},
			finalStatus: "completed",
		},
		{
			name:        "LONG-000-tags-cancel-target",
			jobID:       7201,
			labels:      []string{"w1"},
			tags:        []string{commonTag, cancelTag, "org:chaos-b"},
			finalStatus: "cancelled",
		},
		{
			name:        "LONG-000-tags-cancel-except",
			jobID:       7202,
			labels:      []string{"w2"},
			tags:        []string{commonTag, cancelTag, exceptTag, "org:chaos-b"},
			finalStatus: "completed",
		},
	}
	for _, item := range tasks {
		taskID, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
			TaskName: item.name,
			JobID:    item.jobID,
			SleepMs:  50,
			DelayMs:  2000,
			Group:    "tag-control",
			Labels:   item.labels,
			Tags:     item.tags,
		})
		if err != nil {
			return err
		}
		state.tasks[item.name] = &chaosTaskSlot{taskID: taskID, uniqueTag: item.name, finalStatus: item.finalStatus}
		state.tasksSubmitted++
	}

	if err := user.PauseTasksByTags(ctx, []string{commonTag, pauseTag}, []string{exceptTag}); err != nil {
		return err
	}
	if err := user.ExpectPaused(ctx, "LONG-000-tags-pause-target", 20*time.Second); err != nil {
		return err
	}
	state.userPauses++
	if err := user.ResumeTasksByTags(ctx, []string{commonTag, pauseTag}, []string{exceptTag}); err != nil {
		return err
	}
	if err := user.ExpectPending(ctx, "LONG-000-tags-pause-target", 20*time.Second); err != nil {
		return err
	}
	state.userResumes++
	if err := user.CancelTasksByTags(ctx, []string{commonTag, cancelTag}, []string{exceptTag}); err != nil {
		return err
	}
	if err := user.ExpectCancelled(ctx, "LONG-000-tags-cancel-target", 20*time.Second); err != nil {
		return err
	}
	state.userCancels++

	for _, name := range []string{
		"LONG-000-tags-pause-target",
		"LONG-000-tags-pause-except",
		"LONG-000-tags-cancel-except",
	} {
		if err := user.ExpectCompleted(ctx, name, 30*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func runInitialInfiniteRetryFairness(ctx context.Context, user *User, state *chaosState) error {
	if user == nil || user.DB == nil {
		return fmt.Errorf("user is not ready")
	}
	retryMaxAttempts := int32(-1)
	failers := []string{"LONG-000-retry-loop-a", "LONG-000-retry-loop-b"}
	for idx, name := range failers {
		taskID, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
			TaskName:         name,
			JobID:            int64(8100 + idx),
			SleepMs:          0,
			Group:            "retry-fairness",
			FailMode:         "always",
			Labels:           []string{"retry-fairness"},
			Tags:             []string{"chaos:retry-fairness", "retry:infinite"},
			RetryInterval:    "2s",
			RetryMaxAttempts: &retryMaxAttempts,
		})
		if err != nil {
			return err
		}
		state.tasks[name] = &chaosTaskSlot{taskID: taskID, uniqueTag: name, finalStatus: "cancelled"}
		state.tasksSubmitted++
	}
	for _, name := range failers {
		if err := user.DB.WaitTaskAttemptsAtLeast(ctx, name, 2, 10*time.Second); err != nil {
			return err
		}
	}

	normalName := "LONG-000-retry-fairness-normal"
	normalID, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
		TaskName: normalName,
		JobID:    8199,
		SleepMs:  10,
		Group:    "retry-fairness",
		Labels:   []string{"retry-fairness"},
		Tags:     []string{"chaos:retry-fairness", "retry:normal"},
	})
	if err != nil {
		return err
	}
	state.tasks[normalName] = &chaosTaskSlot{taskID: normalID, uniqueTag: normalName, finalStatus: "completed"}
	state.tasksSubmitted++
	if err := user.ExpectCompleted(ctx, normalName, 5*time.Second); err != nil {
		return fmt.Errorf("normal task starved behind infinite retry tasks: %w", err)
	}

	for _, name := range failers {
		if err := user.CancelTask(ctx, name); err != nil {
			return err
		}
		if err := user.ExpectCancelled(ctx, name, 20*time.Second); err != nil {
			return err
		}
		state.userCancels++
	}
	return nil
}

func submitChaosBatch(ctx context.Context, user *User, state *chaosState, iter int) error {
	batchSize := readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_BATCH_SIZE", 3)
	taskSleepMs := int32(readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_TASK_SLEEP_MS", 400))
	pauseDelayMs := int32(readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_CONTROL_DELAY_MS", 1500))
	pauseSleepMs := int32(readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_CONTROL_SLEEP_MS", 800))
	cancelMaxDelayMs := readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_CANCEL_MAX_DELAY_MS", maxInt(1, int(taskSleepMs)*3))
	for j := 0; j < batchSize; j++ {
		taskName := fmt.Sprintf("LONG-%03d-%02d", iter, j)
		labels, group := taskLabelsAndGroup(j)
		taskID, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
			TaskName: taskName,
			JobID:    int64(iter*100 + j),
			SleepMs:  taskSleepMs,
			Group:    group,
			Labels:   labels,
		})
		if err != nil {
			return err
		}
		state.tasks[taskName] = &chaosTaskSlot{taskID: taskID, uniqueTag: taskName, finalStatus: "completed"}
		state.tasksSubmitted++
	}

	cancelTaskName := fmt.Sprintf("LONG-%03d-cancel", iter)
	labels, group := taskLabelsAndGroup(iter + 1)
	cancelDelayMs := state.randBetween(0, cancelMaxDelayMs)
	cancelTaskID, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
		TaskName: cancelTaskName,
		JobID:    int64(iter*1000 + 81),
		SleepMs:  taskSleepMs,
		Group:    group,
		Labels:   labels,
	})
	if err != nil {
		return err
	}
	cancelAt := time.Now().Add(time.Duration(cancelDelayMs) * time.Millisecond)
	state.tasks[cancelTaskName] = &chaosTaskSlot{taskID: cancelTaskID, uniqueTag: cancelTaskName, controlEligible: true, controlMode: "cancel", finalStatus: "completed", cancelAt: cancelAt}
	state.tasksSubmitted++
	if user.Report != nil {
		user.Report.AddEvent("chaos.cancel_scheduled", cancelTaskName, "cancel scheduled", map[string]any{"taskID": cancelTaskID, "delayMs": cancelDelayMs, "cancelAt": cancelAt.UTC().Format(time.RFC3339Nano)})
	}

	pauseTaskName := fmt.Sprintf("LONG-%03d-pause", iter)
	labels, group = taskLabelsAndGroup(iter)
	pauseTaskID, err := user.SubmitStressProbe(ctx, SubmitStressProbeRequest{
		TaskName: pauseTaskName,
		JobID:    int64(iter*1000 + 91),
		SleepMs:  pauseSleepMs,
		DelayMs:  pauseDelayMs,
		Group:    group,
		Labels:   labels,
	})
	if err != nil {
		return err
	}
	state.tasks[pauseTaskName] = &chaosTaskSlot{taskID: pauseTaskID, uniqueTag: pauseTaskName, controlEligible: true, controlMode: "pause", finalStatus: "completed"}
	state.tasksSubmitted++

	return nil
}

func taskLabelsAndGroup(i int) ([]string, string) {
	switch i % 3 {
	case 1:
		return []string{"w1"}, "w1"
	case 2:
		return []string{"w2"}, "w2"
	default:
		return nil, "default"
	}
}

func runUserOperation(ctx context.Context, user *User, state *chaosState, iter int) error {
	if applied, err := actionUserCancel(ctx, user, state); err != nil || applied {
		return err
	}
	if applied, err := actionUserResume(ctx, user, state, iter); err != nil || applied {
		return err
	}
	if iter%4 == 0 {
		if applied, err := actionUserPause(ctx, user, state, iter); err != nil || applied {
			return err
		}
	}
	return nil
}

func actionUserPause(ctx context.Context, user *User, state *chaosState, iter int) (bool, error) {
	if !state.controlPlaneAvailable(iter) || state.activeWorkers() < 3 {
		return false, nil
	}
	taskName := fmt.Sprintf("LONG-%03d-pause", iter)
	task := state.tasks[taskName]
	if task == nil || !task.controlEligible || task.controlMode != "pause" || task.finalStatus != "completed" || task.controlState != "" {
		return false, nil
	}
	status, err := user.DB.TaskStatus(ctx, task.uniqueTag)
	if err != nil || status != "pending" {
		return false, err
	}
	if err := user.PauseTask(ctx, task.uniqueTag); err != nil {
		return false, err
	}
	if err := user.ExpectPaused(ctx, task.uniqueTag, 20*time.Second); err != nil {
		return false, err
	}
	task.controlState = "paused"
	task.resumeAt = iter + state.randBetween(2, 4)
	state.userPauses++
	return true, nil
}

func actionUserCancel(ctx context.Context, user *User, state *chaosState) (bool, error) {
	if user == nil || user.DB == nil || state.activeWorkers() < 3 {
		return false, nil
	}
	now := time.Now()
	candidates := make([]*chaosTaskSlot, 0)
	for _, task := range state.tasks {
		if task == nil || !task.controlEligible || task.controlMode != "cancel" || task.controlState != "" {
			continue
		}
		if task.cancelAt.IsZero() || task.cancelAt.After(now) {
			continue
		}
		candidates = append(candidates, task)
	}
	if len(candidates) == 0 {
		return false, nil
	}
	chosen := oldestDueCancelTask(candidates)
	if chosen == nil {
		return false, nil
	}
	statusBefore, err := user.DB.TaskStatus(ctx, chosen.uniqueTag)
	if err != nil {
		return false, err
	}
	var snapshot *SignalSnapshot
	if user.Signals != nil {
		snapshot, _ = user.SignalSnapshot(ctx, chosen.taskID)
	}
	if user.Report != nil {
		details := map[string]any{"taskID": chosen.taskID, "statusBefore": statusBefore, "scheduledAt": chosen.cancelAt.UTC().Format(time.RFC3339Nano)}
		if snapshot != nil {
			details["signalCountBefore"] = snapshot.Count
		}
		user.Report.AddEvent("user.cancel_attempt", chosen.uniqueTag, "cancel requested", details)
	}
	if err := user.CancelTask(ctx, chosen.uniqueTag); err != nil {
		return false, err
	}
	statusAfter, err := user.WaitForTask(ctx, chosen.taskID, 20*time.Second)
	if err != nil {
		return false, err
	}
	if statusAfter == "cancelled" && snapshot != nil && snapshot.Count > 0 && user.Signals != nil {
		if err := user.ExpectSignalsStopped(ctx, chosen.taskID, 750*time.Millisecond); err != nil {
			return false, err
		}
	}
	chosen.finalStatus = statusAfter
	chosen.controlState = "cancelled"
	chosen.resumeAt = 0
	state.userCancels++
	return true, nil
}

func oldestChaosTask(tasks []*chaosTaskSlot) *chaosTaskSlot {
	var oldest *chaosTaskSlot
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if oldest == nil || task.taskID < oldest.taskID {
			oldest = task
		}
	}
	return oldest
}

func oldestDueCancelTask(tasks []*chaosTaskSlot) *chaosTaskSlot {
	var chosen *chaosTaskSlot
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if chosen == nil || task.cancelAt.Before(chosen.cancelAt) || (task.cancelAt.Equal(chosen.cancelAt) && task.taskID < chosen.taskID) {
			chosen = task
		}
	}
	return chosen
}

func waitForRunningSignals(ctx context.Context, user *User, task *chaosTaskSlot, timeout time.Duration) (bool, *SignalSnapshot, error) {
	if user == nil || user.DB == nil || user.Signals == nil || task == nil {
		return false, nil, nil
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := user.DB.TaskStatus(ctx, task.uniqueTag)
		if err == nil && status != "completed" && status != "cancelled" && status != "paused" && status != "failed" {
			snapshot, err := user.SignalSnapshot(ctx, task.taskID)
			if err == nil && snapshot != nil && snapshot.Count > 0 {
				return true, snapshot, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false, nil, nil
}

func actionUserResume(ctx context.Context, user *User, state *chaosState, iter int) (bool, error) {
	task, err := state.randomTaskByStatus(ctx, user.DB, []string{"paused"}, func(task *chaosTaskSlot) bool {
		return task.controlMode == "pause" && task.controlState == "paused" && task.resumeAt > 0 && iter >= task.resumeAt
	})
	if err != nil || task == nil {
		return false, err
	}
	if err := user.ResumeTask(ctx, task.uniqueTag); err != nil {
		return false, err
	}
	if err := user.ExpectPending(ctx, task.uniqueTag, 20*time.Second); err != nil {
		return false, err
	}
	task.controlState = "resumed"
	task.resumeAt = 0
	state.userResumes++
	return true, nil
}

func resumeRemainingPausedTasks(ctx context.Context, user *User, state *chaosState, iter int) error {
	for {
		task, err := state.randomTaskByStatus(ctx, user.DB, []string{"paused"}, func(task *chaosTaskSlot) bool {
			return task.controlMode == "pause" && task.controlState == "paused"
		})
		if err != nil {
			return err
		}
		if task == nil {
			return nil
		}
		if err := user.ResumeTask(ctx, task.uniqueTag); err != nil {
			return err
		}
		if err := user.ExpectPending(ctx, task.uniqueTag, 20*time.Second); err != nil {
			return err
		}
		task.controlState = "resumed"
		task.resumeAt = iter
		state.userResumes++
	}
}

func reconcileChaosState(ctx context.Context, h *Harness, state *chaosState, iter int) error {
	if state.controlPlaneDownUntil > 0 && iter > state.controlPlaneDownUntil {
		if err := h.StartControlPlane(ctx); err != nil {
			return err
		}
		state.componentRestarts[h.controlPlaneName]++
		state.controlPlaneDownUntil = 0
	}
	for _, slot := range state.workers {
		if slot == nil || slot.retired || slot.active || slot.restartAt == 0 || slot.restartAt > iter {
			continue
		}
		if err := h.StartWorker(ctx, slot.name, slot.labels); err != nil {
			return err
		}
		state.componentRestarts[slot.name]++
		if err := h.WaitWorkerOnline(ctx, slot.name, true, 20*time.Second); err != nil {
			return err
		}
		slot.active = true
		slot.restartAt = 0
	}
	return nil
}

func runRandomAction(ctx context.Context, h *Harness, state *chaosState, iter int) error {
	disruptEvery := readChaosPositiveEnvInt(nil, "ANCLAX_TASKCORE_CHAOS_DISRUPT_EVERY", 3)
	actions := []func(context.Context, *Harness, *chaosState, int) (bool, error){
		actionWorkerRejoinEarly,
		actionRuntimeConfig,
	}
	if disruptEvery <= 1 || iter%disruptEvery == 0 {
		actions = append(actions,
			actionWorkerDown,
			actionWorkerRetireAndReplace,
			actionControlPlaneDown,
			actionPostgresRestart,
		)
	}
	start := state.rng.Intn(len(actions))
	for i := 0; i < len(actions); i++ {
		applied, err := actions[(start+i)%len(actions)](ctx, h, state, iter)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return nil
}

func actionWorkerDown(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	slot := state.randomActiveWorker()
	if slot == nil {
		return false, nil
	}
	if err := h.StopWorker(ctx, slot.name, false); err != nil {
		return false, err
	}
	slot.active = false
	slot.restartAt = iter + state.randBetween(2, 5)
	state.workerDisruptions++
	state.componentDowns[slot.name]++
	h.Report().AddEvent("chaos.worker_down", slot.name, "worker stopped", map[string]any{"restartAt": slot.restartAt})
	return true, nil
}

func actionWorkerRetireAndReplace(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	slot := state.randomActiveWorker()
	if slot == nil {
		return false, nil
	}
	if err := h.StopWorker(ctx, slot.name, true); err != nil {
		return false, err
	}
	slot.active = false
	slot.retired = true
	state.componentDowns[slot.name]++
	state.nextReplacement++
	repl := &chaosWorkerSlot{
		name:      fmt.Sprintf("worker-repl-%02d", state.nextReplacement),
		labels:    append([]string(nil), slot.labels...),
		restartAt: iter + state.randBetween(2, 4),
	}
	state.workers = append(state.workers, repl)
	state.workerDisruptions++
	state.replacementWorkers++
	h.Report().AddEvent("chaos.worker_replace", slot.name, "worker retired and replacement scheduled", map[string]any{"replacement": repl.name, "restartAt": repl.restartAt})
	return true, nil
}

func actionControlPlaneDown(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	if !state.controlPlaneAvailable(iter) {
		return false, nil
	}
	if err := h.StopControlPlane(ctx); err != nil {
		return false, err
	}
	state.controlPlaneDownUntil = iter + state.randBetween(2, 4)
	state.controlPlaneOutages++
	state.componentDowns[h.controlPlaneName]++
	h.Report().AddEvent("chaos.control_plane_down", h.controlPlaneName, "control plane stopped", map[string]any{"restartAt": state.controlPlaneDownUntil})
	return true, nil
}

func actionRuntimeConfig(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	if !state.controlPlaneAvailable(iter) || state.activeWorkers() == 0 {
		return false, nil
	}
	req := RuntimeConfigRequest{
		MaxStrictPercentage: int32(state.randBetween(0, 60)),
		DefaultWeight:       1,
		LabelWeights: map[string]int32{
			"w1": int32(state.randBetween(1, 4)),
			"w2": int32(state.randBetween(1, 4)),
		},
	}
	taskID, err := h.User().Control.StartUpdateRuntimeConfig(ctx, req)
	if err != nil {
		return false, err
	}
	status, err := h.User().WaitForTask(ctx, taskID, 0)
	if err != nil {
		return false, err
	}
	if status != "completed" {
		return false, fmt.Errorf("runtime config task %d status=%s want=completed", taskID, status)
	}
	state.runtimeConfigUpdates++
	h.Report().AddEvent("chaos.runtime_config", "control-plane", "runtime config updated", map[string]any{"iter": iter, "taskID": taskID, "weights": req.LabelWeights})
	return true, nil
}

func actionPostgresRestart(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	if err := h.RestartPostgres(ctx); err != nil {
		return false, err
	}
	state.componentDowns[h.postgresName]++
	state.componentRestarts[h.postgresName]++
	for _, slot := range state.workers {
		if slot == nil || slot.retired || !slot.active {
			continue
		}
		if err := h.StopWorker(ctx, slot.name, true); err != nil {
			return false, err
		}
		slot.active = false
		state.componentDowns[slot.name]++
		if state.rng.Intn(100) < 20 {
			slot.retired = true
			state.nextReplacement++
			state.replacementWorkers++
			state.workers = append(state.workers, &chaosWorkerSlot{
				name:      fmt.Sprintf("worker-repl-%02d", state.nextReplacement),
				labels:    append([]string(nil), slot.labels...),
				restartAt: iter + state.randBetween(2, 4),
			})
		} else {
			slot.restartAt = iter + state.randBetween(2, 5)
		}
	}
	state.postgresRestarts++
	h.Report().AddEvent("chaos.postgres_restart", h.postgresName, "postgres restarted", map[string]any{"iter": iter})
	return true, nil
}

func actionWorkerRejoinEarly(ctx context.Context, h *Harness, state *chaosState, iter int) (bool, error) {
	slot := state.randomRestartableWorker(iter)
	if slot == nil {
		return false, nil
	}
	if err := h.StartWorker(ctx, slot.name, slot.labels); err != nil {
		return false, err
	}
	state.componentRestarts[slot.name]++
	if err := h.WaitWorkerOnline(ctx, slot.name, true, 20*time.Second); err != nil {
		return false, err
	}
	slot.active = true
	slot.restartAt = 0
	h.Report().AddEvent("chaos.worker_rejoin", slot.name, "worker rejoined early", map[string]any{"iter": iter})
	return true, nil
}

func restoreCluster(ctx context.Context, h *Harness, state *chaosState, iter int) error {
	if !state.controlPlaneAvailable(iter) {
		if err := h.StartControlPlane(ctx); err != nil {
			return err
		}
		state.componentRestarts[h.controlPlaneName]++
		state.controlPlaneDownUntil = 0
	}
	for _, slot := range state.workers {
		if slot == nil || slot.retired || slot.active {
			continue
		}
		slot.restartAt = iter
	}
	return reconcileChaosState(ctx, h, state, iter)
}

func (s *chaosState) controlPlaneAvailable(iter int) bool {
	return s.controlPlaneDownUntil == 0 || iter > s.controlPlaneDownUntil
}

func (s *chaosState) randBetween(min int, max int) int {
	if max <= min {
		return min
	}
	return min + s.rng.Intn(max-min+1)
}

func (s *chaosState) randomActiveWorker() *chaosWorkerSlot {
	candidates := make([]*chaosWorkerSlot, 0)
	for _, slot := range s.workers {
		if slot != nil && slot.active && !slot.retired {
			candidates = append(candidates, slot)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[s.rng.Intn(len(candidates))]
}

func (s *chaosState) randomRestartableWorker(iter int) *chaosWorkerSlot {
	candidates := make([]*chaosWorkerSlot, 0)
	for _, slot := range s.workers {
		if slot == nil || slot.retired || slot.active {
			continue
		}
		if slot.restartAt > iter {
			candidates = append(candidates, slot)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[s.rng.Intn(len(candidates))]
}

func (s *chaosState) activeWorkers() int {
	count := 0
	for _, slot := range s.workers {
		if slot != nil && slot.active && !slot.retired {
			count++
		}
	}
	return count
}

func (s *chaosState) randomTaskByStatus(ctx context.Context, inspector *Inspector, statuses []string, predicate func(*chaosTaskSlot) bool) (*chaosTaskSlot, error) {
	if inspector == nil {
		return nil, nil
	}
	allowed := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		allowed[status] = struct{}{}
	}
	candidates := make([]*chaosTaskSlot, 0)
	for _, task := range s.tasks {
		if task != nil && predicate(task) {
			candidates = append(candidates, task)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	for _, idx := range s.rng.Perm(len(candidates)) {
		task := candidates[idx]
		status, err := inspector.TaskStatus(ctx, task.uniqueTag)
		if err != nil {
			continue
		}
		if _, ok := allowed[status]; ok {
			return task, nil
		}
	}
	return nil, nil
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func readChaosPositiveEnvInt(t *testing.T, key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if t != nil {
		require.NoErrorf(t, err, "invalid integer env %s=%q", key, raw)
		require.Greaterf(t, v, 0, "env %s must be > 0", key)
	} else {
		if err != nil || v <= 0 {
			return fallback
		}
	}
	return v
}

func readChaosInt64Env(t *testing.T, key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if t != nil {
		require.NoErrorf(t, err, "invalid int64 env %s=%q", key, raw)
	} else if err != nil {
		return fallback
	}
	return v
}
