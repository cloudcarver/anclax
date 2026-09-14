package ctrl

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	"github.com/jackc/pgx/v5"
)

// TagConcurrency describes global admission across all workers. A nil limit
// means unlimited, with InUse zero. For configured limits, InUse includes
// admitted attempts awaiting finalization or lease recovery, and can temporarily
// exceed a newly lowered limit.
type TagConcurrency struct {
	Tag            string
	MaxConcurrency *int32
	InUse          int32
}

// SetTagConcurrencyLimit applies to existing and future business tasks with
// this tag. Zero blocks new attempts; lowering a limit does not interrupt
// running handlers. Tasks with multiple tags must satisfy every limit.
func (s *WorkerControlPlane) SetTagConcurrencyLimit(ctx context.Context, tag string, maxConcurrency int32) error {
	if tag == "" {
		return errors.New("tag cannot be empty")
	}
	if maxConcurrency < 0 {
		return errors.New("tag concurrency limit must be non-negative")
	}
	if err := s.model.SetTaskTagConcurrencyLimit(ctx, querier.SetTaskTagConcurrencyLimitParams{Tag: tag, MaxConcurrency: maxConcurrency}); err != nil {
		return fmt.Errorf("set tag concurrency limit: %w", err)
	}
	return nil
}

// RemoveTagConcurrencyLimit restores unlimited admission and clears its permits
// and counter. Re-enabling backfills the still-leased attempt snapshots.
func (s *WorkerControlPlane) RemoveTagConcurrencyLimit(ctx context.Context, tag string) error {
	if tag == "" {
		return errors.New("tag cannot be empty")
	}
	if err := s.model.RemoveTaskTagConcurrencyLimit(ctx, tag); err != nil {
		return fmt.Errorf("remove tag concurrency limit: %w", err)
	}
	return nil
}

func (s *WorkerControlPlane) GetTagConcurrency(ctx context.Context, tag string) (*TagConcurrency, error) {
	if tag == "" {
		return nil, errors.New("tag cannot be empty")
	}
	row, err := s.model.GetTaskTagConcurrency(ctx, tag)
	if errors.Is(err, pgx.ErrNoRows) {
		return &TagConcurrency{Tag: tag}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tag concurrency: %w", err)
	}
	return &TagConcurrency{Tag: row.Tag, MaxConcurrency: row.MaxConcurrency, InUse: row.InUse}, nil
}

// ListTagConcurrencyLimits returns configured tags in lexical order. Use the
// last returned tag as afterTag for the next page; an empty page ends the scan.
func (s *WorkerControlPlane) ListTagConcurrencyLimits(ctx context.Context, afterTag string, pageSize int32) ([]TagConcurrency, error) {
	if pageSize < 1 || pageSize > 1000 {
		return nil, errors.New("page size must be between 1 and 1000")
	}
	rows, err := s.model.ListTaskTagConcurrencyLimits(ctx, querier.ListTaskTagConcurrencyLimitsParams{AfterTag: afterTag, PageSize: pageSize})
	if err != nil {
		return nil, fmt.Errorf("list tag concurrency limits: %w", err)
	}
	result := make([]TagConcurrency, 0, len(rows))
	for _, row := range rows {
		result = append(result, TagConcurrency{Tag: row.Tag, MaxConcurrency: row.MaxConcurrency, InUse: row.InUse})
	}
	return result, nil
}
