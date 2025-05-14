package hooks

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type OnOrgCreatedWithTx func(ctx context.Context, tx pgx.Tx, orgID int32) error

type AnchorHookInterface interface {
	OnOrgCreatedWithTx(ctx context.Context, tx pgx.Tx, orgID int32) error

	// RegisterOnOrgCreatedHook registers a hook function that is executed after an organization is created.
	// It should be implemented to be idempotent and compatible with at-least-once execution semantics,
	// as it may be called multiple times for the same organization.
	RegisterOnOrgCreatedHook(hook OnOrgCreatedWithTx)
}

type BaseHook struct {
	OnOrgCreatedWithTxHooks []OnOrgCreatedWithTx
}

func NewBaseHook() AnchorHookInterface {
	return &BaseHook{}
}

func (b *BaseHook) RegisterOnOrgCreatedHook(hook OnOrgCreatedWithTx) {
	b.OnOrgCreatedWithTxHooks = append(b.OnOrgCreatedWithTxHooks, hook)
}

func (b *BaseHook) OnOrgCreatedWithTx(ctx context.Context, tx pgx.Tx, orgID int32) error {
	for _, hook := range b.OnOrgCreatedWithTxHooks {
		if err := hook(ctx, tx, orgID); err != nil {
			return err
		}
	}
	return nil
}
