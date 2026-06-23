package store

import (
	"context"
	"time"
)

type KeyStore interface {
	// Create creates a new key and returns the keyID.
	Create(ctx context.Context, key []byte, ttl time.Duration, group string) (int64, error)

	// Get returns the key for the given keyID. returns ErrKeyNotFound if the key is not found.
	Get(ctx context.Context, keyID int64) ([]byte, error)

	// Delete deletes the key for the given keyID. returns ErrKeyNotFound if the key is not found.
	Delete(ctx context.Context, keyID int64) error

	// DeleteGroupKeys deletes all keys for the given group.
	DeleteGroupKeys(ctx context.Context, group string) error
}
