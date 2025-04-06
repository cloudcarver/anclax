package controller

import (
	"errors"

	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/auth"
	"github.com/cloudcarver/anchor/internal/service"
	"github.com/gofiber/fiber/v2"
)

type Controller struct {
	svc  service.ServiceInterface
	auth auth.AuthInterface
}

var _ apigen.ServerInterface = &Controller{}

func NewController(
	s service.ServiceInterface,
	auth auth.AuthInterface,
) *Controller {
	return &Controller{
		svc:  s,
		auth: auth,
	}
}

func (controller *Controller) SignIn(c *fiber.Ctx) error {
	var params apigen.SignInRequest
	if err := c.BodyParser(&params); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	credentials, err := controller.svc.SignIn(c.Context(), params)
	if err != nil {
		if errors.Is(err, service.ErrInvalidPassword) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return err
	}

	return c.Status(fiber.StatusOK).JSON(credentials)
}

func (controller *Controller) RefreshToken(c *fiber.Ctx) error {
	var params apigen.RefreshTokenRequest
	if err := c.BodyParser(&params); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	userID, err := controller.auth.ValidateRefreshToken(c.Context(), params.RefreshToken)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	credentials, err := controller.svc.CreateToken(c.Context(), userID)
	if err != nil {
		if errors.Is(err, service.ErrRefreshTokenExpired) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return err
	}

	return c.Status(fiber.StatusOK).JSON(credentials)
}

func (controller *Controller) GetJWKS(c *fiber.Ctx) error {
	jwks, err := controller.auth.GetJWKS()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	return c.Status(fiber.StatusOK).JSON(jwks)
}
