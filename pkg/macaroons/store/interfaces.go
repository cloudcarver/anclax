package store

import (
	"context"
	"time"
)

type KeyStore interface {
	// RunTransaction executes f atomically. The KeyStore passed to f is bound to
	// the transaction and must not be retained after f returns.
	RunTransaction(ctx context.Context, f func(KeyStore) error) error

	// Create creates a new key and returns the keyID.
	Create(ctx context.Context, key []byte, ttl time.Duration, group string) (int64, error)

	// Get returns the key for the given keyID. returns ErrKeyNotFound if the key is not found.
	Get(ctx context.Context, keyID int64) ([]byte, error)

	// Consume deletes a live key only when both its ID and secret match. This is
	// the atomic single-use primitive used by refresh-token rotation.
	Consume(ctx context.Context, keyID int64, key []byte) error

	// Delete deletes the key for the given keyID. returns ErrKeyNotFound if the key is not found.
	Delete(ctx context.Context, keyID int64) error

	// DeleteGroupKeys deletes all keys for the given group.
	DeleteGroupKeys(ctx context.Context, group string) error
}
