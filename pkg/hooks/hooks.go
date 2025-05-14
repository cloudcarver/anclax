package hooks

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type OnOrgCreatedWithTx func(ctx context.Context, tx pgx.Tx, orgID int32) error

// There are two types of hooks:
// 1. Tx hooks: These hooks are executed with a transaction.
// 2. Async hooks: These hooks are executed asynchronously using the task runner.
type AnchorHookInterface interface {
	OnOrgCreatedWithTx(ctx context.Context, tx pgx.Tx, orgID int32) error

	// RegisterOnOrgCreatedHook registers a hook function that is executed after an organization is created.
	RegisterOnOrgCreatedWithTx(hook OnOrgCreatedWithTx)
}

type BaseHook struct {
	OnOrgCreatedWithTxHooks []OnOrgCreatedWithTx
}

func NewBaseHook() AnchorHookInterface {
	return &BaseHook{}
}

func (b *BaseHook) RegisterOnOrgCreatedWithTx(hook OnOrgCreatedWithTx) {
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
