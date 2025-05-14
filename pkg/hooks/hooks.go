package hooks

import (
	"context"
)

type OnOrgCreated func(ctx context.Context, orgID int32) error

type AnchorHookInterface interface {
	OnOrgCreated(ctx context.Context, orgID int32) error
}

type BaseHook struct {
	OnOrgCreatedHooks []OnOrgCreated
}

func NewBaseHook() AnchorHookInterface {
	return &BaseHook{}
}

// RegisterOnOrgCreatedHook registers a hook function that is executed after an organization is created.
// It should be implemented to be idempotent and compatible with at-least-once execution semantics,
// as it may be called multiple times for the same organization.
func (b *BaseHook) RegisterOnOrgCreatedHook(hook OnOrgCreated) {
	b.OnOrgCreatedHooks = append(b.OnOrgCreatedHooks, hook)
}

func (b *BaseHook) OnOrgCreated(ctx context.Context, orgID int32) error {
	for _, hook := range b.OnOrgCreatedHooks {
		if err := hook(ctx, orgID); err != nil {
			return err
		}
	}
	return nil
}
