package store

import (
	"context"
	"time"
)

type KeyStore interface {
	// Create creates a new key and returns the keyID.
	Create(ctx context.Context, userID int32, key []byte, ttl time.Duration) (int64, error)

	// Get returns the key for the given keyID. returns ErrKeyNotFound if the key is not found.
	Get(ctx context.Context, keyID int64) ([]byte, error)

	// Delete deletes the key for the given keyID. returns ErrKeyNotFound if the key is not found.
	Delete(ctx context.Context, keyID int64) error

	// DeleteUserKeys deletes all keys for the given userID.
	DeleteUserKeys(ctx context.Context, userID int32) error
}
