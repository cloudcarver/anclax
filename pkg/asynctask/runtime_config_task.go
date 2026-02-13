package asynctask

import (
	"context"
	"math"

	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

const ConfigUpdateTaskPriority int32 = math.MaxInt32

func RunUpdateWorkerRuntimeConfigTask(ctx context.Context, runner taskgen.TaskRunner, params *taskgen.UpdateWorkerRuntimeConfigParameters, overrides ...taskcore.TaskOverride) (int32, error) {
	allOverrides := append([]taskcore.TaskOverride{}, overrides...)
	allOverrides = append(allOverrides, taskcore.WithPriority(ConfigUpdateTaskPriority))
	return runner.RunUpdateWorkerRuntimeConfig(ctx, params, allOverrides...)
}
