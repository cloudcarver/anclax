package store

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/core"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/querier"
	runner "github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
)

var (
	ErrKeyNotFound = errors.New("key not found")
)

type Store struct {
	model      model.ModelInterface
	taskRunner runner.TaskRunner
	tx         core.Tx
	inTx       bool
}

func NewStore(model model.ModelInterface, taskRunner runner.TaskRunner) KeyStore {
	return &Store{
		model:      model,
		taskRunner: taskRunner,
	}
}

func (s *Store) RunTransaction(ctx context.Context, f func(KeyStore) error) error {
	if s.inTx {
		return f(s)
	}

	return s.model.RunTransactionWithTx(ctx, func(tx core.Tx, txm model.ModelInterface) error {
		return f(&Store{
			model:      txm,
			taskRunner: s.taskRunner,
			tx:         tx,
			inTx:       true,
		})
	})
}

func (s *Store) Create(ctx context.Context, key []byte, ttl time.Duration, group string) (int64, error) {
	if !s.inTx {
		var keyID int64
		if err := s.RunTransaction(ctx, func(txs KeyStore) error {
			var err error
			keyID, err = txs.Create(ctx, key, ttl, group)
			return err
		}); err != nil {
			return 0, err
		}
		return keyID, nil
	}

	var groupPtr *string
	if group != "" {
		groupPtr = &group
	}
	created, err := s.model.CreateOpaqueKey(ctx, querier.CreateOpaqueKeyParams{
		Group:           groupPtr,
		Key:             key,
		TtlMicroseconds: ttl.Microseconds(),
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to create key")
	}

	if ttl > 0 {
		if _, err := s.taskRunner.RunDeleteOpaqueKeyWithTx(ctx, s.tx, &runner.DeleteOpaqueKeyParameters{
			KeyID: created.ID,
		}, taskcore.WithStartedAt(created.ExpiresAt)); err != nil {
			return 0, errors.Wrap(err, "failed to run task to delete key")
		}
	}
	return created.ID, nil
}

func (s *Store) Get(ctx context.Context, keyID int64) ([]byte, error) {
	key, err := s.model.GetOpaqueKey(ctx, keyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrKeyNotFound
		}
		return nil, errors.Wrap(err, "failed to get key")
	}

	return key, nil
}

func (s *Store) Consume(ctx context.Context, keyID int64, key []byte) error {
	if _, err := s.model.ConsumeOpaqueKey(ctx, querier.ConsumeOpaqueKeyParams{
		ID:  keyID,
		Key: key,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrKeyNotFound
		}
		return errors.Wrap(err, "failed to consume key")
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, keyID int64) error {
	err := s.model.DeleteOpaqueKey(ctx, keyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrKeyNotFound
		}
		return errors.Wrap(err, "failed to delete key")
	}
	return nil
}

func (s *Store) DeleteGroupKeys(ctx context.Context, group string) error {
	if group == "" {
		return nil
	}
	err := s.model.DeleteOpaqueKeys(ctx, &group)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrKeyNotFound
		}
		return errors.Wrap(err, "failed to delete group keys")
	}
	return nil
}
