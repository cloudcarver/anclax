package chaos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Inspector struct {
	pool *pgxpool.Pool
}

type TaskSnapshot struct {
	UniqueTag string     `json:"uniqueTag"`
	Type      string     `json:"type"`
	Status    string     `json:"status"`
	Attempts  int32      `json:"attempts"`
	WorkerID  *string    `json:"workerID,omitempty"`
	LockedAt  *time.Time `json:"lockedAt,omitempty"`
}

func NewInspector(ctx context.Context, dsn string) (*Inspector, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Inspector{pool: pool}, nil
}

func (i *Inspector) Close() {
	if i != nil && i.pool != nil {
		i.pool.Close()
	}
}

func (i *Inspector) WaitForTaskStatus(ctx context.Context, uniqueTag string, status string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := i.TaskStatus(ctx, uniqueTag)
		if err == nil && got == status {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	got, err := i.TaskStatus(ctx, uniqueTag)
	if err != nil {
		return err
	}
	return fmt.Errorf("task %s status=%s want=%s", uniqueTag, got, status)
}

func (i *Inspector) TaskStatus(ctx context.Context, uniqueTag string) (string, error) {
	var status string
	err := i.pool.QueryRow(ctx, `
		select status
		from anclax.tasks
		where unique_tag = $1
		order by created_at desc
		limit 1
	`, uniqueTag).Scan(&status)
	if err != nil {
		return "", err
	}
	return status, nil
}

func (i *Inspector) TaskStatusByID(ctx context.Context, taskID int32) (string, error) {
	var status string
	err := i.pool.QueryRow(ctx, `
		select status
		from anclax.tasks
		where id = $1
	`, taskID).Scan(&status)
	if err != nil {
		return "", err
	}
	return status, nil
}

func (i *Inspector) TaskAttempts(ctx context.Context, uniqueTag string) (int32, error) {
	var attempts int32
	err := i.pool.QueryRow(ctx, `
		select attempts
		from anclax.tasks
		where unique_tag = $1
		order by created_at desc
		limit 1
	`, uniqueTag).Scan(&attempts)
	return attempts, err
}

func (i *Inspector) WaitTaskAttemptsAtLeast(ctx context.Context, uniqueTag string, minAttempts int32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		attempts, err := i.TaskAttempts(ctx, uniqueTag)
		if err == nil && attempts >= minAttempts {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	attempts, err := i.TaskAttempts(ctx, uniqueTag)
	if err != nil {
		return err
	}
	return fmt.Errorf("task %s attempts=%d want>=%d", uniqueTag, attempts, minAttempts)
}

func (i *Inspector) WaitForTaskByID(ctx context.Context, taskID int32, timeout time.Duration) (string, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for deadline.IsZero() || time.Now().Before(deadline) {
		status, err := i.TaskStatusByID(ctx, taskID)
		if err == nil {
			switch status {
			case "completed", "failed", "cancelled", "paused":
				return status, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	status, err := i.TaskStatusByID(ctx, taskID)
	if err != nil {
		return "", err
	}
	return status, fmt.Errorf("task %d status=%s not terminal", taskID, status)
}

func (i *Inspector) CountTasks(ctx context.Context, prefix string) (int64, error) {
	var count int64
	err := i.pool.QueryRow(ctx, `
		select count(*)
		from anclax.tasks
		where unique_tag like $1
	`, prefix+"%").Scan(&count)
	return count, err
}

func (i *Inspector) CountTasksByStatuses(ctx context.Context, statuses []string, prefix string) (int64, error) {
	var count int64
	err := i.pool.QueryRow(ctx, `
		select count(*)
		from anclax.tasks
		where status = any($1::text[])
		  and unique_tag like $2
	`, statuses, prefix+"%").Scan(&count)
	return count, err
}

func (i *Inspector) CountRetriedTasks(ctx context.Context, prefix string) (int64, error) {
	var count int64
	err := i.pool.QueryRow(ctx, `
		select count(*)
		from anclax.tasks
		where unique_tag like $1
		  and attempts >= 2
	`, prefix+"%").Scan(&count)
	return count, err
}

func (i *Inspector) SnapshotTasks(ctx context.Context, prefix string, limit int) ([]TaskSnapshot, error) {
	rows, err := i.pool.Query(ctx, `
		select coalesce(unique_tag, ''), spec->>'type', status, attempts, nullif(worker_id::text, ''), locked_at
		from anclax.tasks
		where unique_tag like $1
		order by created_at desc
		limit $2
	`, prefix+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TaskSnapshot, 0)
	for rows.Next() {
		var item TaskSnapshot
		var workerID *string
		var lockedAt *time.Time
		if err := rows.Scan(&item.UniqueTag, &item.Type, &item.Status, &item.Attempts, &workerID, &lockedAt); err != nil {
			return nil, err
		}
		item.WorkerID = workerID
		item.LockedAt = lockedAt
		out = append(out, item)
	}
	return out, rows.Err()
}

func (i *Inspector) WaitWorkerOnline(ctx context.Context, workerName string, expected bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var count int64
		err := i.pool.QueryRow(ctx, `
			select count(*)
			from anclax.workers
			where status = 'online'
			  and labels @> to_jsonb($1::text[])
		`, []string{"chaos:" + workerName}).Scan(&count)
		if err == nil && ((count > 0) == expected) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("worker %s online=%v not reached", workerName, expected)
}

func (i *Inspector) DumpDiagnostics(ctx context.Context, artifactDir string, prefix string) error {
	if err := os.MkdirAll(filepath.Join(artifactDir, "db"), 0o755); err != nil {
		return err
	}
	tasks, err := i.SnapshotTasks(ctx, prefix, 200)
	if err == nil {
		if raw, merr := json.MarshalIndent(tasks, "", "  "); merr == nil {
			_ = os.WriteFile(filepath.Join(artifactDir, "db", "tasks.json"), raw, 0o644)
		}
	}
	return nil
}
