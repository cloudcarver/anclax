package asynctask

import (
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewExecutorWithoutDSNDisablesAckWaiter(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	exec := NewExecutor(&config.Config{}, model.NewMockModelInterface(ctrl), taskgen.NewMockTaskRunner(ctrl))
	require.Nil(t, exec.waitForAck)
	require.Equal(t, 9*time.Second, exec.runtimeConfigHeartbeatTTL)
}
