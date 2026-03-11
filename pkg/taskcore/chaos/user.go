package chaos

import (
	"context"
	"fmt"
	"time"
)

type User struct {
	Control *ControlPlaneClient
	DB      *Inspector
	Report  *Report
}

func (u *User) SubmitStressProbe(ctx context.Context, req SubmitStressProbeRequest) error {
	if req.TaskName == "" {
		return fmt.Errorf("task name is required")
	}
	if req.UniqueTag == "" {
		req.UniqueTag = req.TaskName
	}
	taskID, err := u.Control.SubmitStressProbe(ctx, req)
	if err != nil {
		return err
	}
	if u.Report != nil {
		u.Report.AddEvent("user.submit_task", req.TaskName, "stress probe submitted", map[string]any{
			"taskID":  taskID,
			"group":   req.Group,
			"labels":  req.Labels,
			"sleepMs": req.SleepMs,
		})
	}
	return nil
}

func (u *User) ExpectCompleted(ctx context.Context, uniqueTag string, timeout time.Duration) error {
	if err := u.DB.WaitForTaskStatus(ctx, uniqueTag, "completed", timeout); err != nil {
		return err
	}
	if u.Report != nil {
		u.Report.AddEvent("user.expectation", uniqueTag, "task completed", nil)
	}
	return nil
}
