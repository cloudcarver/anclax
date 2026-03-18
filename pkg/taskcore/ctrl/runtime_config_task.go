package ctrl

import (
	"context"
	"errors"
	"fmt"

	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/uuid"
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

func (r *UpdateWorkerRuntimeConfigRequest) toTaskParams() (*taskgen.BroadcastUpdateWorkerRuntimeConfigParameters, error) {
	if r == nil {
		return nil, nil
	}
	workerIDs := make([]uuid.UUID, 0, len(r.WorkerIDs))
	for _, rawWorkerID := range r.WorkerIDs {
		if rawWorkerID == "" {
			continue
		}
		workerID, err := uuid.Parse(rawWorkerID)
		if err != nil {
			return nil, fmt.Errorf("invalid workerID %q: %w", rawWorkerID, err)
		}
		workerIDs = append(workerIDs, workerID)
	}
	return &taskgen.BroadcastUpdateWorkerRuntimeConfigParameters{
		MaxStrictPercentage: r.MaxStrictPercentage,
		DefaultWeight:       r.DefaultWeight,
		Labels:              append([]string(nil), r.Labels...),
		Weights:             append([]int32(nil), r.Weights...),
		WorkerIDs:           workerIDs,
	}, nil
}

func RunUpdateWorkerRuntimeConfigTask(ctx context.Context, runner taskgen.TaskRunner, req *UpdateWorkerRuntimeConfigRequest, overrides ...taskcore.TaskOverride) (int32, error) {
	if req == nil {
		return 0, errors.New("update worker runtime config request cannot be nil")
	}
	params, err := req.toTaskParams()
	if err != nil {
		return 0, err
	}
	allOverrides := append([]taskcore.TaskOverride{}, overrides...)
	allOverrides = append(allOverrides, taskcore.WithPriority(WorkerControlTaskPriority))
	return runner.RunBroadcastUpdateWorkerRuntimeConfig(ctx, params, allOverrides...)
}
