package controller

import (
	"errors"

	"github.com/cloudcarver/anchor/pkg/auth"
	"github.com/cloudcarver/anchor/pkg/service"
	"github.com/cloudcarver/anchor/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v2"
)

type Controller struct {
	svc  service.ServiceInterface
	auth auth.AuthInterface
}

func NewController(
	s service.ServiceInterface,
	auth auth.AuthInterface,
) apigen.ServerInterface {
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

func (controller *Controller) SignOut(c *fiber.Ctx) error {
	userID, err := auth.GetUserID(c)
	if err != nil {
		return c.SendStatus(fiber.StatusUnauthorized)
	}
	return controller.auth.InvalidateUserTokens(c.Context(), userID)
}

func (controller *Controller) RefreshToken(c *fiber.Ctx) error {
	var params apigen.RefreshTokenRequest
	if err := c.BodyParser(&params); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	userID, err := controller.auth.ParseRefreshToken(c.Context(), params.RefreshToken)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	credentials, err := controller.svc.RefreshToken(c.Context(), userID, params.RefreshToken)
	if err != nil {
		if errors.Is(err, service.ErrRefreshTokenExpired) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return err
	}

	return c.Status(fiber.StatusOK).JSON(credentials)
}

func (controller *Controller) ListTasks(c *fiber.Ctx) error {
	ret, err := controller.svc.ListTasks(c.Context())
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusOK).JSON(ret)
}

func (controller *Controller) ListEvents(c *fiber.Ctx) error {
	ret, err := controller.svc.ListEvents(c.Context())
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusOK).JSON(ret)
}

func (controller *Controller) ListOrgs(c *fiber.Ctx) error {
	userID, err := auth.GetUserID(c)
	if err != nil {
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	ret, err := controller.svc.ListOrgs(c.Context(), userID)
	if err != nil {
		return err
	}

	return c.Status(fiber.StatusOK).JSON(ret)
}

func (controller *Controller) TryExecuteTask(c *fiber.Ctx, taskID int32) error {
	return nil
}
