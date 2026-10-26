//go:build taskclientintegration

package taskclient_test

import (
	"context"
	"os"
	"testing"

	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/jackc/pgx/v5"
)

type clientExecutor struct {
	taskgen.ExecutorInterface
	group string
}

func (e *clientExecutor) ExecuteStressProbe(_ context.Context, _ worker.Task, params *taskgen.StressProbeParameters) error {
	e.group = params.Group
	return nil
}

func TestGeneratedTasksUseGoStoreAndHandler(t *testing.T) {
	dsn := os.Getenv("ANCLAX_TASK_CLIENT_TEST_DSN")
	if dsn == "" {
		t.Fatal("run through pkg/codegen/task/clienttests/integration.py")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	queries := querier.New(conn)
	taskStore := store.NewTaskStore(&model.Model{Querier: queries})
	for _, language := range []string{"python", "node"} {
		task, err := taskStore.GetTaskByUniqueTag(ctx, language+"-go")
		if err != nil {
			t.Fatal(err)
		}
		if task.Attributes.RetryPolicy.Interval != "1s" || task.Attributes.RetryPolicy.MaxAttempts != 1 || *task.Attributes.Timeout != "30s" {
			t.Fatalf("%s task defaults do not match the Go spec: %+v", language, task.Attributes)
		}
		executor := &clientExecutor{}
		handler := taskgen.NewTaskHandler(executor)
		if err := handler.HandleTask(ctx, worker.Task{ID: task.ID, Spec: task.Spec, Attributes: task.Attributes}); err != nil {
			t.Fatal(err)
		}
		if executor.group != language {
			t.Fatalf("Go handler received %q, expected %q", executor.group, language)
		}
		if err := taskStore.CancelTask(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
		cancelled, err := taskStore.GetTaskByID(ctx, task.ID)
		if err != nil || cancelled.UniqueTag != nil {
			t.Fatalf("generated client releaseUniqueTagOnTerminal was not honored: %v, %+v", err, cancelled)
		}
	}
	_, err = taskgen.NewTaskRunner(taskStore).RunStressProbe(ctx, &taskgen.StressProbeParameters{
		JobID: 42, SleepMs: 0, Group: "go",
	}, store.WithUniqueTag("from-go"))
	if err != nil {
		t.Fatal(err)
	}
}
