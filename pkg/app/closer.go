package app

import (
	"context"
	"slices"
	"time"

	"go.uber.org/zap"
)

const (
	DefaultGracefulShutdownTimeout = 5 * time.Second
)

type Closer func(ctx context.Context) error

type CloserManager struct {
	closers []Closer
}

func NewCloserManager() *CloserManager {
	return &CloserManager{}
}

func (cm *CloserManager) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultGracefulShutdownTimeout)
	defer cancel()

	slices.Reverse(cm.closers)

	for _, closer := range cm.closers {
		if err := closer(ctx); err != nil {
			log.Error("error in graceful shutdown", zap.Error(err))
		}
	}
}

func (cm *CloserManager) Register(closers ...Closer) {
	cm.closers = append(cm.closers, closers...)
}
