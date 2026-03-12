package chaos

import (
	"context"
	"fmt"
	"time"
)

type User struct {
	Control       *ControlPlaneClient
	Signals       *SignalClient
	SignalBaseURL string
	DB            *Inspector
	Report        *Report
}

func (u *User) SubmitStressProbe(ctx context.Context, req SubmitStressProbeRequest) (int32, error) {
	if req.TaskName == "" {
		return 0, fmt.Errorf("task name is required")
	}
	if req.UniqueTag == "" {
		req.UniqueTag = req.TaskName
	}
	taskID, err := u.Control.SubmitStressProbe(ctx, req)
	if err != nil {
		return 0, err
	}
	if u.Report != nil {
		u.Report.AddEvent("user.submit_task", req.TaskName, "stress probe submitted", map[string]any{
			"taskID":           taskID,
			"group":            req.Group,
			"labels":           req.Labels,
			"sleepMs":          req.SleepMs,
			"delayMs":          req.DelayMs,
			"uniqueTag":        req.UniqueTag,
			"signalBaseURL":    req.SignalBaseURL,
			"signalIntervalMs": req.SignalIntervalMs,
		})
	}
	return taskID, nil
}

func (u *User) PauseTask(ctx context.Context, uniqueTag string) error {
	if uniqueTag == "" {
		return fmt.Errorf("unique tag is required")
	}
	if err := u.Control.PauseTask(ctx, uniqueTag); err != nil {
		return err
	}
	if u.Report != nil {
		u.Report.AddEvent("user.pause_task", uniqueTag, "task paused", nil)
	}
	return nil
}

func (u *User) CancelTask(ctx context.Context, uniqueTag string) error {
	if uniqueTag == "" {
		return fmt.Errorf("unique tag is required")
	}
	if err := u.Control.CancelTask(ctx, uniqueTag); err != nil {
		return err
	}
	if u.Report != nil {
		u.Report.AddEvent("user.cancel_task", uniqueTag, "task cancelled", nil)
	}
	return nil
}

func (u *User) ResumeTask(ctx context.Context, uniqueTag string) error {
	if uniqueTag == "" {
		return fmt.Errorf("unique tag is required")
	}
	if err := u.Control.ResumeTask(ctx, uniqueTag); err != nil {
		return err
	}
	if u.Report != nil {
		u.Report.AddEvent("user.resume_task", uniqueTag, "task resumed", nil)
	}
	return nil
}

func (u *User) SignalSnapshot(ctx context.Context, taskID int32) (*SignalSnapshot, error) {
	if u.Signals == nil {
		return nil, fmt.Errorf("signal client is not configured")
	}
	return u.Signals.Snapshot(ctx, taskID)
}

func (u *User) WaitForSignals(ctx context.Context, taskID int32, minCount int64, timeout time.Duration) (*SignalSnapshot, error) {
	if minCount <= 0 {
		minCount = 1
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snapshot, err := u.SignalSnapshot(ctx, taskID)
		if err == nil && snapshot != nil && snapshot.Count >= minCount {
			if u.Report != nil {
				u.Report.AddEvent("user.expectation", fmt.Sprintf("%d", taskID), "signal threshold reached", map[string]any{"taskID": taskID, "count": snapshot.Count})
			}
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	snapshot, err := u.SignalSnapshot(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("task %d signal count=%d want>=%d", taskID, snapshot.Count, minCount)
}

func (u *User) ExpectSignalsStopped(ctx context.Context, taskID int32, quietWindow time.Duration) error {
	if quietWindow <= 0 {
		quietWindow = 750 * time.Millisecond
	}
	deadline := time.Now().Add(4 * quietWindow)
	last, err := u.SignalSnapshot(ctx, taskID)
	if err != nil {
		return err
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(quietWindow):
		}
		next, err := u.SignalSnapshot(ctx, taskID)
		if err != nil {
			return err
		}
		if next.Count == last.Count {
			if u.Report != nil {
				u.Report.AddEvent("user.expectation", fmt.Sprintf("%d", taskID), "signals stopped", map[string]any{"taskID": taskID, "count": next.Count, "quietWindow": quietWindow.String()})
			}
			return nil
		}
		last = next
	}
	return fmt.Errorf("task %d signals still increasing after cancel, last_count=%d", taskID, last.Count)
}

func (u *User) ExpectStatus(ctx context.Context, uniqueTag string, status string, timeout time.Duration) error {
	if err := u.DB.WaitForTaskStatus(ctx, uniqueTag, status, timeout); err != nil {
		return err
	}
	if u.Report != nil {
		u.Report.AddEvent("user.expectation", uniqueTag, fmt.Sprintf("task status=%s", status), map[string]any{"status": status})
	}
	return nil
}

func (u *User) ExpectCompleted(ctx context.Context, uniqueTag string, timeout time.Duration) error {
	return u.ExpectStatus(ctx, uniqueTag, "completed", timeout)
}

func (u *User) ExpectCancelled(ctx context.Context, uniqueTag string, timeout time.Duration) error {
	return u.ExpectStatus(ctx, uniqueTag, "cancelled", timeout)
}

func (u *User) ExpectPaused(ctx context.Context, uniqueTag string, timeout time.Duration) error {
	return u.ExpectStatus(ctx, uniqueTag, "paused", timeout)
}

func (u *User) ExpectPending(ctx context.Context, uniqueTag string, timeout time.Duration) error {
	return u.ExpectStatus(ctx, uniqueTag, "pending", timeout)
}
