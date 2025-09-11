package store

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/pkg/taskcore"
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
	now        func() time.Time
}

func NewStore(model model.ModelInterface, taskRunner runner.TaskRunner) KeyStore {
	return &Store{
		model:      model,
		taskRunner: taskRunner,
		now:        time.Now,
	}
}

func (s *Store) Create(ctx context.Context, userID int32, key []byte, ttl time.Duration) (int64, error) {
	var ret int64
	if err := s.model.RunTransaction(ctx, func(txm model.ModelInterface) error {
		keyID, err := txm.CreateOpaqueKey(ctx, querier.CreateOpaqueKeyParams{
			UserID: userID,
			Key:    key,
		})
		if err != nil {
			return errors.Wrap(err, "failed to create key")
		}

		ret = keyID

		if ttl > 0 {
			if _, err := s.taskRunner.RunDeleteOpaqueKey(ctx, &runner.DeleteOpaqueKeyParameters{
				KeyID: keyID,
			}, taskcore.WithStartedAt(s.now().Add(ttl))); err != nil {
				return errors.Wrap(err, "failed to run task to delete key")
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return ret, nil
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

func (s *Store) DeleteUserKeys(ctx context.Context, userID int32) error {
	err := s.model.DeleteOpaqueKeys(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrKeyNotFound
		}
		return errors.Wrap(err, "failed to delete user keys")
	}
	return nil
}
