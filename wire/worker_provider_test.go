package wire

import (
	"testing"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/stretchr/testify/require"
)

func TestNewConfiguredWorker_DefaultUsesWorker(t *testing.T) {
	gctx := globalctx.New()
	t.Cleanup(gctx.Cancel)

	w, err := NewConfiguredWorker(gctx, &config.Config{}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, w)
	_, ok := w.(*worker.Worker)
	require.True(t, ok)
}
