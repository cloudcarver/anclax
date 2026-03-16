package ctrl

import (
	"context"
	"errors"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

// WorkerControlTaskPriority keeps internal worker-control tasks claimable even
// when runtime config sets maxStrictPercentage=0 and workers stop issuing
// strict claims.
const WorkerControlTaskPriority int32 = 0

type UpdateWorkerRuntimeConfigRequest struct {
	MaxStrictPercentage *int32
	DefaultWeight       *int32
	Labels              []string
	Weights             []int32
	WorkerIDs           []string
}

func (r *UpdateWorkerRuntimeConfigRequest) toTaskParams() *taskgen.BroadcastUpdateWorkerRuntimeConfigParameters {
	if r == nil {
		return nil
	}
	return &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{
		MaxStrictPercentage: r.MaxStrictPercentage,
		DefaultWeight:       r.DefaultWeight,
		Labels:              append([]string(nil), r.Labels...),
		Weights:             append([]int32(nil), r.Weights...),
		WorkerIDs:           append([]string(nil), r.WorkerIDs...),
	}
}

func RunUpdateWorkerRuntimeConfigTask(ctx context.Context, runner taskgen.TaskRunner, req *UpdateWorkerRuntimeConfigRequest, overrides ...taskcore.TaskOverride) (int32, error) {
	if req == nil {
		return 0, errors.New("update worker runtime config request cannot be nil")
	}
	params := req.toTaskParams()
	allOverrides := append([]taskcore.TaskOverride{}, overrides...)
	allOverrides = append(allOverrides, taskcore.WithPriority(WorkerControlTaskPriority))
	return runner.RunBroadcastUpdateWorkerRuntimeConfig(ctx, params, allOverrides...)
}
