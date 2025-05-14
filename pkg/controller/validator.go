package controller

import (
	"github.com/cloudcarver/anchor/pkg/auth"
	"github.com/cloudcarver/anchor/pkg/zcore/model"
	"github.com/cloudcarver/anchor/pkg/zgen/apigen"
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
