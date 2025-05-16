package hooks

import (
	"context"

	"github.com/cloudcarver/anchor/pkg/macaroons"
	"github.com/jackc/pgx/v5"
)

type (
	OnOrgCreatedWithTx func(ctx context.Context, tx pgx.Tx, orgID int32) error

	OnCreateToken func(ctx context.Context, userID int32, macaroon *macaroons.Macaroon) error
)

// There are two types of hooks:
// 1. Tx hooks: These hooks are executed with a transaction.
// 2. Async hooks: These hooks are executed asynchronously using the task runner.
type AnchorHookInterface interface {
	OnOrgCreatedWithTx(ctx context.Context, tx pgx.Tx, orgID int32) error

	OnCreateToken(ctx context.Context, userID int32, macaroon *macaroons.Macaroon) error

	// RegisterOnOrgCreatedHook registers a hook function that is executed after an organization is created.
	RegisterOnOrgCreatedWithTx(hook OnOrgCreatedWithTx)

	// RegisterOnCreateToken registers a hook function that is executed after a token is created.
	// You can add caveats to the token.
	RegisterOnCreateToken(hook OnCreateToken)
}

type BaseHook struct {
	OnOrgCreatedWithTxHooks []OnOrgCreatedWithTx
	OnCreateTokenHooks      []OnCreateToken
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

func (b *BaseHook) RegisterOnCreateToken(hook OnCreateToken) {
	b.OnCreateTokenHooks = append(b.OnCreateTokenHooks, hook)
}

func (b *BaseHook) OnCreateToken(ctx context.Context, userID int32, macaroon *macaroons.Macaroon) error {
	for _, hook := range b.OnCreateTokenHooks {
		if err := hook(ctx, userID, macaroon); err != nil {
			return err
		}
	}
	return nil
}
