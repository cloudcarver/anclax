//go:build smoke
// +build smoke

package taskcore_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/jackc/pgx/v5"
)

const (
	smokeContainerName      = "anclax-pg-smoke"
	smokePort               = "5499"
	runtimeConfigAckChannel = "anclax_worker_runtime_config_ack"
)

func withSmokePostgres(t *testing.T, fn func(ctx context.Context, m model.ModelInterface)) {
	t.Helper()
	if !dockerAvailable() {
		t.Skip("docker not available")
	}

	cleanupContainer(t)
	if err := runDocker(t, "run", "-d", "--name", smokeContainerName,
		"-e", "POSTGRES_PASSWORD=postgres",
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_DB=postgres",
		"-p", smokePort+":5432",
		"postgres:15",
	); err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}
	t.Cleanup(func() { cleanupContainer(t) })

	dsn := smokePostgresDSN()
	if err := waitForPostgres(t, dsn, 15*time.Second); err != nil {
		t.Fatalf("postgres not ready: %v", err)
	}

	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}}
	libCfg := config.DefaultLibConfig()
	cm := closer.NewCloserManager()
	t.Cleanup(cm.Close)

	m, err := model.NewModel(cfg, libCfg, cm)
	if err != nil {
		t.Fatalf("failed to create model: %v", err)
	}

	fn(context.Background(), m)
}

func smokePostgresDSN() string {
	return "postgres://postgres:postgres@localhost:" + smokePort + "/postgres?sslmode=disable"
}

func dockerAvailable() bool {
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	return cmd.Run() == nil
}

func runDocker(t *testing.T, args ...string) error {
	t.Helper()
	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %v failed: %w: %s", args, err, string(output))
	}
	return nil
}

func cleanupContainer(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "rm", "-f", smokeContainerName)
	_ = cmd.Run()
}

func waitForPostgres(t *testing.T, dsn string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			_ = conn.Close(ctx)
			cancel()
			return nil
		}
		cancel()
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for postgres")
}

func signalOnce(ch chan struct{}) {
	select {
	case <-ch:
		return
	default:
		close(ch)
	}
}

type smokeWorkerHandler struct {
	startedCh chan struct{}
	proceedCh chan struct{}
	doneCh    chan struct{}
}

func newSmokeWorkerHandler() *smokeWorkerHandler {
	return &smokeWorkerHandler{
		startedCh: make(chan struct{}),
		proceedCh: make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func (h *smokeWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	if spec.GetType() != "smoke-worker" {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.startedCh)
	select {
	case <-h.proceedCh:
	case <-ctx.Done():
		return ctx.Err()
	}
	signalOnce(h.doneCh)
	return nil
}

func (h *smokeWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *smokeWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *smokeWorkerHandler) release() {
	signalOnce(h.proceedCh)
}

func (h *smokeWorkerHandler) started() <-chan struct{} {
	return h.startedCh
}

func (h *smokeWorkerHandler) done() <-chan struct{} {
	return h.doneCh
}

type retryWorkerHandler struct {
	attempts        int32
	firstAttemptCh  chan struct{}
	secondAttemptCh chan struct{}
}

func newRetryWorkerHandler() *retryWorkerHandler {
	return &retryWorkerHandler{
		firstAttemptCh:  make(chan struct{}),
		secondAttemptCh: make(chan struct{}),
	}
}

func (h *retryWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	if spec.GetType() != "smoke-retry" {
		return worker.ErrUnknownTaskType
	}
	attempt := atomic.AddInt32(&h.attempts, 1)
	if attempt == 1 {
		signalOnce(h.firstAttemptCh)
		return errors.New("retry attempt")
	}
	signalOnce(h.secondAttemptCh)
	return nil
}

func (h *retryWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *retryWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *retryWorkerHandler) firstAttempt() <-chan struct{} {
	return h.firstAttemptCh
}

func (h *retryWorkerHandler) secondAttempt() <-chan struct{} {
	return h.secondAttemptCh
}

type cronWorkerHandler struct {
	ranCh chan struct{}
}

func newCronWorkerHandler() *cronWorkerHandler {
	return &cronWorkerHandler{ranCh: make(chan struct{})}
}

func (h *cronWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	if spec.GetType() != "smoke-cron" {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.ranCh)
	return nil
}

func (h *cronWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *cronWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *cronWorkerHandler) ran() <-chan struct{} {
	return h.ranCh
}

type noopWorkerHandler struct{}

func (h *noopWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	return worker.ErrUnknownTaskType
}

func (h *noopWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *noopWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

type blockingWorkerHandler struct {
	taskType  string
	startedCh chan struct{}
	releaseCh chan struct{}
	doneCh    chan struct{}
}

func newBlockingWorkerHandler(taskType string) *blockingWorkerHandler {
	return &blockingWorkerHandler{
		taskType:  taskType,
		startedCh: make(chan struct{}),
		releaseCh: make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func (h *blockingWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.startedCh)
	select {
	case <-h.releaseCh:
	case <-ctx.Done():
		return ctx.Err()
	}
	signalOnce(h.doneCh)
	return nil
}

func (h *blockingWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *blockingWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *blockingWorkerHandler) release() {
	signalOnce(h.releaseCh)
}

func (h *blockingWorkerHandler) started() <-chan struct{} {
	return h.startedCh
}

func (h *blockingWorkerHandler) done() <-chan struct{} {
	return h.doneCh
}

type signalWorkerHandler struct {
	taskType string
	doneCh   chan struct{}
}

func newSignalWorkerHandler(taskType string) *signalWorkerHandler {
	return &signalWorkerHandler{taskType: taskType, doneCh: make(chan struct{})}
}

func (h *signalWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	signalOnce(h.doneCh)
	return nil
}

func (h *signalWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *signalWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	return nil
}

func (h *signalWorkerHandler) done() <-chan struct{} {
	return h.doneCh
}

type failureWorkerHandler struct {
	taskType string
	failErr  error
	failedCh chan struct{}
}

func newFailureWorkerHandler(taskType string, failErr error) *failureWorkerHandler {
	return &failureWorkerHandler{taskType: taskType, failErr: failErr, failedCh: make(chan struct{})}
}

func (h *failureWorkerHandler) HandleTask(ctx context.Context, spec worker.TaskSpec) error {
	if spec.GetType() != h.taskType {
		return worker.ErrUnknownTaskType
	}
	return h.failErr
}

func (h *failureWorkerHandler) RegisterTaskHandler(handler worker.TaskHandler) {}

func (h *failureWorkerHandler) OnTaskFailed(ctx context.Context, tx core.Tx, failedTaskSpec worker.TaskSpec, taskID int32) error {
	signalOnce(h.failedCh)
	return nil
}

func (h *failureWorkerHandler) failed() <-chan struct{} {
	return h.failedCh
}

type runtimeConfigPayloadSpec struct {
	MaxStrictPercentage *int32           `json:"maxStrictPercentage,omitempty"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type runtimeConfigNotifySpec struct {
	Op     string                    `json:"op"`
	Params runtimeConfigNotifyParams `json:"params"`
}

type runtimeConfigNotifyParams struct {
	RequestID string `json:"request_id"`
	Version   int64  `json:"version"`
}

type runtimeConfigAckSpec struct {
	Op     string                 `json:"op"`
	Params runtimeConfigAckParams `json:"params"`
}

type runtimeConfigAckParams struct {
	RequestID      string `json:"request_id"`
	WorkerID       string `json:"worker_id"`
	AppliedVersion int64  `json:"applied_version"`
}
