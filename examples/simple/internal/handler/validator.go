package handler

import (
	"myexampleapp/internal/zgen/apigen"

	"github.com/cloudcarver/anchor/pkg/auth"
	"github.com/gofiber/fiber/v2"
)

type Validator struct {
	auth auth.AuthInterface
}

func NewValidator(auth auth.AuthInterface) apigen.Validator {
	return &Validator{auth}
}

func (v *Validator) PreValidate(c *fiber.Ctx) error {
	if err := v.auth.Authfunc(c); err != nil {
		return fiber.ErrUnauthorized
	}
	return nil
}

func (v *Validator) PostValidate(c *fiber.Ctx) error {
	return nil
}

func (v *Validator) OperationPermit(c *fiber.Ctx, operationID string) error {
	return nil
}
