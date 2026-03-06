package wire

import (
	"testing"

	"github.com/cloudcarver/anclax/pkg/asynctask"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/stretchr/testify/require"
)

func TestNewConfiguredWorkerNilExecutor(t *testing.T) {
	gctx := globalctx.New()
	t.Cleanup(gctx.Cancel)

	w, err := NewConfiguredWorker(gctx, &config.Config{}, nil, nil, nil)
	require.Error(t, err)
	require.Nil(t, w)
}

func TestNewConfiguredWorkerInvalidConfig(t *testing.T) {
	gctx := globalctx.New()
	t.Cleanup(gctx.Cancel)

	badID := "not-uuid"
	cfg := &config.Config{}
	cfg.Worker.WorkerID = &badID
	w, err := NewConfiguredWorker(gctx, cfg, nil, nil, asynctask.NewExecutor(&config.Config{}, nil, nil))
	require.Error(t, err)
	require.Nil(t, w)
}

func TestNewConfiguredWorker_DefaultUsesWorker(t *testing.T) {
	gctx := globalctx.New()
	t.Cleanup(gctx.Cancel)

	w, err := NewConfiguredWorker(gctx, &config.Config{}, nil, nil, asynctask.NewExecutor(&config.Config{}, nil, nil))
	require.NoError(t, err)
	require.NotNil(t, w)
	_, ok := w.(*worker.Worker)
	require.True(t, ok)
}
