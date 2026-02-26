package wire

import (
	"testing"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	legacyworker "github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/taskcore/workerv2"
	"github.com/stretchr/testify/require"
)

func TestNewConfiguredWorker_DefaultUsesWorkerV2(t *testing.T) {
	gctx := globalctx.New()
	t.Cleanup(gctx.Cancel)

	w, err := NewConfiguredWorker(gctx, &config.Config{}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, w)
	_, ok := w.(*workerv2.Worker)
	require.True(t, ok)
}

func TestNewConfiguredWorker_UseLegacyWorker(t *testing.T) {
	gctx := globalctx.New()
	t.Cleanup(gctx.Cancel)

	cfg := &config.Config{}
	cfg.Worker.UseLegacyWorker = true

	w, err := NewConfiguredWorker(gctx, cfg, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, w)
	_, ok := w.(*legacyworker.Worker)
	require.True(t, ok)
}
