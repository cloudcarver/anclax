//go:build taskclientintegration

package sample_test

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSampleTaskClientsE2E(t *testing.T) {
	adminDSN := os.Getenv("ANCLAX_TASK_CLIENT_TEST_DSN")
	require.NotEmpty(t, adminDSN, "run with ANCLAX_TASK_CLIENT_TEST_DSN=<admin URI> make test-sample-e2e")
	sampleDir, err := os.Getwd()
	require.NoError(t, err)
	repoDir := filepath.Clean(filepath.Join(sampleDir, "../.."))
	generatedDir := t.TempDir()

	// Generate into a fresh application directory without go.mod or checked-in
	// clients so neither language can accidentally use a stale generated file.
	require.NoError(t, os.CopyFS(filepath.Join(generatedDir, "api"), os.DirFS(filepath.Join(sampleDir, "api"))))
	for _, name := range []string{"anclax.yaml", "enqueue.py", "enqueue.mjs", "package.json"} {
		data, err := os.ReadFile(filepath.Join(sampleDir, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(generatedDir, name), data, 0o600))
	}
	runSampleCommand(t, repoDir, nil, "go", "run", "./cmd/anclax", "gen", generatedDir)
	for _, name := range []string{"sdks/python/tasks.py", "sdks/nodejs/tasks.mjs", "sdks/nodejs/tasks.d.mts"} {
		generated, err := os.ReadFile(filepath.Join(generatedDir, name))
		require.NoError(t, err)
		checkedIn, err := os.ReadFile(filepath.Join(sampleDir, name))
		require.NoError(t, err)
		require.True(t, bytes.Equal(checkedIn, generated), "%s is stale; run go run ./cmd/anclax gen examples/sample", name)
	}
	modules := filepath.Join(sampleDir, "node_modules")
	_, err = os.Stat(filepath.Join(modules, "pg"))
	require.NoError(t, err, "install the sample's database driver with npm ci --prefix examples/sample")
	require.NoError(t, os.Symlink(modules, filepath.Join(generatedDir, "node_modules")))

	// Both orders matter: each language must insert a new task as well as find
	// a task inserted by the other language using the same unique tag.
	for _, order := range [][]string{{"python", "node"}, {"node", "python"}} {
		t.Run(order[0]+"_first", func(t *testing.T) {
			dsn := newSampleDatabase(t, adminDSN)
			pollInterval := 20 * time.Millisecond
			cfg := &config.Config{
				Pg:     config.Pg{DSN: &dsn},
				Worker: config.Worker{Labels: []string{"email"}, PollInterval: &pollInterval},
			}
			cm := closer.NewCloserManager()
			t.Cleanup(cm.Close)
			m, err := model.NewModel(cfg, config.DefaultLibConfig(), cm)
			require.NoError(t, err)
			env := append(os.Environ(), "DATABASE_URL="+dsn)
			enqueue := func(language string) int32 {
				script := "enqueue.py"
				if language == "node" {
					script = "enqueue.mjs"
				}
				out := runSampleCommand(t, generatedDir, env, language, script)
				id, err := strconv.ParseInt(strings.TrimSpace(out), 10, 32)
				require.NoError(t, err, "%s must print the task ID", script)
				require.Positive(t, id)
				return int32(id)
			}
			taskID := enqueue(order[0])
			require.Equal(t, taskID, enqueue(order[1]), "languages must share unique-tag deduplication")
			pending, err := m.GetTaskByID(t.Context(), taskID)
			require.NoError(t, err)
			require.Equal(t, "pending", pending.Status)

			handler := &recordingEmailHandler{received: make(chan worker.Task, 1)}
			gctx := globalctx.New()
			t.Cleanup(gctx.Cancel)
			w, err := worker.NewWorkerFromConfig(gctx, cfg, m, handler)
			require.NoError(t, err)
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				w.Start()
			}()
			t.Cleanup(func() {
				gctx.Cancel()
				select {
				case <-stopped:
				case <-time.After(10 * time.Second):
					t.Error("sample worker did not stop")
				}
			})

			// Use the normal scheduler and worker loop, including finalization.
			// The test handler records delivery instead of sending a real email.
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				task, err := m.GetTaskByID(t.Context(), taskID)
				require.NoError(c, err)
				require.Equal(c, "completed", task.Status)
			}, 15*time.Second, 20*time.Millisecond, "sample task %d must finish through the Go worker", taskID)
			var delivered worker.Task
			select {
			case delivered = <-handler.received:
			default:
				t.Fatal("task completed without reaching the sample handler")
			}
			require.Equal(t, taskID, delivered.ID)
			require.Equal(t, "sendEmail", delivered.GetType())
			require.JSONEq(t, `{"userID":123,"template":"welcome","variables":{"name":"Alice"}}`, string(delivered.GetPayload()))
			require.Equal(t, &[]string{"email"}, delivered.Attributes.Labels)
			require.Equal(t, &[]string{"sample"}, delivered.Attributes.Tags)
			require.Equal(t, &apigen.TaskRetryPolicy{Interval: "5s", MaxAttempts: 3}, delivered.Attributes.RetryPolicy)
			require.NotNil(t, delivered.Attributes.Timeout)
			require.Equal(t, "30s", *delivered.Attributes.Timeout)

			// The sample retains its unique tag after completion. Running either
			// producer again must return the completed task without enqueueing work.
			for _, language := range order {
				require.Equal(t, taskID, enqueue(language))
			}
			conn, err := pgx.Connect(t.Context(), dsn)
			require.NoError(t, err)
			defer conn.Close(context.Background())
			var count int
			require.NoError(t, conn.QueryRow(t.Context(), "SELECT count(*) FROM anclax.tasks WHERE spec->>'type' = 'sendEmail'").Scan(&count))
			require.Equal(t, 1, count)
			require.Equal(t, int32(1), handler.calls.Load())
		})
	}
}

func runSampleCommand(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Env = dir, env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	require.NoError(t, err, "%s failed:\n%s\n%s", args[0], stdout, stderr.String())
	return string(stdout)
}

func newSampleDatabase(t *testing.T, adminDSN string) string {
	t.Helper()
	uri, err := url.Parse(adminDSN)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, uri.Scheme, "admin DSN must be a PostgreSQL URI")
	name := "anclax_sample_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	admin, err := pgx.Connect(t.Context(), adminDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		assert.NoError(t, err)
	})
	uri.Path, uri.RawPath = "/"+name, ""
	return uri.String()
}

type recordingEmailHandler struct {
	calls    atomic.Int32
	received chan worker.Task
}

func (h *recordingEmailHandler) HandleTask(_ context.Context, task worker.Task) error {
	if task.GetType() != "sendEmail" {
		return worker.ErrUnknownTaskType
	}
	if h.calls.Add(1) == 1 {
		h.received <- task
	}
	return nil
}

func (*recordingEmailHandler) OnTaskFailed(context.Context, core.Tx, worker.TaskSpec, int32) error {
	return nil
}

func (*recordingEmailHandler) RegisterTaskHandler(worker.TaskHandler) {}
