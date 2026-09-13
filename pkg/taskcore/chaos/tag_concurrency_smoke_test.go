//go:build smoke

package chaos

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
)

func chaosConcurrencyTags(group string, iter, slot int) []string {
	// Limit one in three slots, rotating every three iterations so each routing
	// group and the pause/cancel probes see both limited and unlimited traffic.
	// Keep this independent of the RNG used to choose faults.
	if (slot+(iter-1)/3)%3 != 0 {
		return nil
	}
	return []string{"chaos:concurrency:global", "chaos:concurrency:group:" + group}
}

func installTagConcurrencyAudit(ctx context.Context, inspector *Inspector) error {
	q := querier.New(inspector.pool)
	for tag, limit := range map[string]int32{
		"chaos:concurrency:global":        3,
		"chaos:concurrency:group:default": 2,
		"chaos:concurrency:group:w1":      2,
		"chaos:concurrency:group:w2":      2,
	} {
		if err := q.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: tag, MaxConcurrency: limit}); err != nil {
			return err
		}
	}
	// Persist every admission observation across Postgres and worker restarts.
	// Sampling the live counter alone could miss brief oversubscription.
	_, err := inspector.pool.Exec(ctx, `
        CREATE TABLE anclax.chaos_tag_admissions (
            tag text NOT NULL, in_use int NOT NULL, max_concurrency int,
            permits int NOT NULL, observed_at timestamptz NOT NULL DEFAULT clock_timestamp()
        );
        CREATE FUNCTION anclax.chaos_record_tag_admission() RETURNS TRIGGER LANGUAGE plpgsql AS $$
        BEGIN
            IF NEW.tag LIKE 'chaos:concurrency:%' AND NEW.in_use > OLD.in_use THEN
                INSERT INTO anclax.chaos_tag_admissions(tag, in_use, max_concurrency, permits)
                SELECT NEW.tag, NEW.in_use, NEW.max_concurrency, count(*)
                FROM anclax.task_tag_permits WHERE tag = NEW.tag;
            END IF;
            RETURN NEW;
        END $$;
        CREATE TRIGGER chaos_record_tag_admission AFTER UPDATE OF in_use ON anclax.task_tag_concurrency
        FOR EACH ROW EXECUTE FUNCTION anclax.chaos_record_tag_admission();
    `)
	return err
}

func checkTagConcurrencyAudit(ctx context.Context, inspector *Inspector, report *Report) error {
	var limitedTasks, unlimitedTasks int64
	if err := inspector.pool.QueryRow(ctx, `SELECT
        count(*) FILTER (WHERE COALESCE(attributes->'tags' ? 'chaos:concurrency:global', false)),
        count(*) FILTER (WHERE NOT COALESCE(attributes->'tags' ? 'chaos:concurrency:global', false))
        FROM anclax.tasks WHERE unique_tag LIKE 'LONG-%' AND unique_tag NOT LIKE 'LONG-000-%'`).Scan(&limitedTasks, &unlimitedTasks); err != nil {
		return err
	}
	if limitedTasks == 0 || unlimitedTasks == 0 {
		return fmt.Errorf("expected mixed chaos workload: limited=%d unlimited=%d", limitedTasks, unlimitedTasks)
	}
	var observations, violations, peak, groupPeak int64
	if err := inspector.pool.QueryRow(ctx, `SELECT count(*),
        count(*) FILTER (WHERE in_use > max_concurrency OR in_use <> permits),
        COALESCE(max(in_use) FILTER (WHERE tag = 'chaos:concurrency:global'), 0),
        COALESCE(max(in_use) FILTER (WHERE tag LIKE 'chaos:concurrency:group:%'), 0)
        FROM anclax.chaos_tag_admissions`).Scan(&observations, &violations, &peak, &groupPeak); err != nil {
		return err
	}
	if observations == 0 || violations != 0 {
		return fmt.Errorf("tag concurrency audit: observations=%d violations=%d", observations, violations)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var remaining, mismatches int64
		err := inspector.pool.QueryRow(ctx, `SELECT COALESCE(sum(c.in_use), 0), count(*) FILTER (WHERE c.in_use <> p.n)
            FROM anclax.task_tag_concurrency c
            CROSS JOIN LATERAL (SELECT count(*) AS n FROM anclax.task_tag_permits p WHERE p.tag = c.tag) p
            WHERE c.tag LIKE 'chaos:concurrency:%'`).Scan(&remaining, &mismatches)
		if err != nil {
			return err
		}
		if mismatches != 0 {
			return fmt.Errorf("tag counters drifted: %d mismatches", mismatches)
		}
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tag permits did not drain: %d", remaining)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	var terminalMemberships int64
	if err := inspector.pool.QueryRow(ctx, `SELECT count(*)
        FROM anclax.task_tags tt JOIN anclax.tasks t ON t.id = tt.task_id
        WHERE t.status NOT IN ('pending', 'running', 'paused') AND t.locked_at IS NULL`).Scan(&terminalMemberships); err != nil {
		return err
	}
	if terminalMemberships != 0 {
		return fmt.Errorf("terminal tasks retained tag membership: %d", terminalMemberships)
	}
	report.AddEvent("assert.tag_concurrency", "postgres", "all admissions within limits; permits drained; terminal membership cleaned", map[string]any{
		"limitedTasks": limitedTasks, "unlimitedTasks": unlimitedTasks,
		"observations": observations, "violations": violations, "globalPeak": peak, "groupPeak": groupPeak,
		"terminalMemberships": terminalMemberships,
	})
	return nil
}
