package controller

import (
	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/auth"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/gofiber/fiber/v2"
)

type Validator struct {
	model model.ModelInterface
	auth  auth.AuthInterface
}

func NewValidator(model model.ModelInterface, auth auth.AuthInterface) apigen.Validator {
	return &Validator{model: model, auth: auth}
}

func (v *Validator) GetOrgID(c *fiber.Ctx) int32 {
	return c.Locals(auth.ContextKeyOrgID).(int32)
}

func (v *Validator) PreValidate(c *fiber.Ctx) error {
	return v.auth.Authfunc(c)
}

func (v *Validator) PostValidate(c *fiber.Ctx) error {
	return nil
}
