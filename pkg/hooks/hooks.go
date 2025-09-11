package hooks

import (
	"context"

	"github.com/cloudcarver/anclax/pkg/macaroons"
	"github.com/jackc/pgx/v5"
)

type (
	OnOrgCreated func(ctx context.Context, tx pgx.Tx, orgID int32) error

	OnCreateToken func(ctx context.Context, userID int32, macaroon *macaroons.Macaroon) error

	OnUserCreated func(ctx context.Context, tx pgx.Tx, userID int32) error
)

// There are two types of hooks:
// 1. Tx hooks: These hooks are executed with a transaction.
// 2. Async hooks: These hooks are executed asynchronously using the task runner.
type AnclaxHookInterface interface {
	OnOrgCreated(ctx context.Context, tx pgx.Tx, orgID int32) error

	OnCreateToken(ctx context.Context, userID int32, macaroon *macaroons.Macaroon) error

	OnUserCreated(ctx context.Context, tx pgx.Tx, userID int32) error

	// RegisterOnOrgCreatedHook registers a hook function that is executed after an organization is created.
	RegisterOnOrgCreated(hook OnOrgCreated)

	// RegisterOnCreateToken registers a hook function that is executed after a token is created.
	// You can add caveats to the token.
	RegisterOnCreateToken(hook OnCreateToken)

	RegisterOnUserCreated(hook OnUserCreated)
}

type BaseHook struct {
	OnOrgCreatedHooks  []OnOrgCreated
	OnCreateTokenHooks []OnCreateToken
	OnUserCreatedHooks []OnUserCreated
}

func NewBaseHook() AnclaxHookInterface {
	return &BaseHook{}
}

func (b *BaseHook) RegisterOnOrgCreated(hook OnOrgCreated) {
	b.OnOrgCreatedHooks = append(b.OnOrgCreatedHooks, hook)
}

func (b *BaseHook) OnOrgCreated(ctx context.Context, tx pgx.Tx, orgID int32) error {
	for _, hook := range b.OnOrgCreatedHooks {
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

func (b *BaseHook) RegisterOnUserCreated(hook OnUserCreated) {
	b.OnUserCreatedHooks = append(b.OnUserCreatedHooks, hook)
}

func (b *BaseHook) OnUserCreated(ctx context.Context, tx pgx.Tx, userID int32) error {
	for _, hook := range b.OnUserCreatedHooks {
		if err := hook(ctx, tx, userID); err != nil {
			return err
		}
	}
	return nil
}
