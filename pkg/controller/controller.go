package controller

import (
	"errors"

	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/service"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
	"github.com/gofiber/fiber/v3"
)

type Controller struct {
	svc  service.ServiceInterface
	auth auth.AuthInterface

	enableWorkerHTTPTrigger bool
	disableDefaultSignUp    bool
}

func NewController(
	s service.ServiceInterface,
	auth auth.AuthInterface,
	cfg *config.Config,
) apigen.ServerInterface {
	return &Controller{
		svc:                     s,
		auth:                    auth,
		enableWorkerHTTPTrigger: cfg.Worker.EnableHTTPTrigger,
		disableDefaultSignUp:    cfg.DisableDefaultSignUp,
	}
}

func (controller *Controller) SignIn(c fiber.Ctx) error {
	var params apigen.SignInRequest
	if err := c.Bind().Body(&params); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	credentials, err := controller.svc.SignInWithPassword(c.Context(), params)
	if err != nil {
		if errors.Is(err, service.ErrInvalidPassword) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return err
	}

	return c.Status(fiber.StatusOK).JSON(credentials)
}

func (controller *Controller) SignOut(c fiber.Ctx) error {
	userID, err := auth.GetUserID(c)
	if err != nil {
		return c.SendStatus(fiber.StatusUnauthorized)
	}
	return controller.auth.InvalidateUserTokens(c.Context(), userID)
}

func (controller *Controller) RefreshToken(c fiber.Ctx) error {
	var params apigen.RefreshTokenRequest
	if err := c.Bind().Body(&params); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	credentials, err := controller.svc.RefreshToken(c.Context(), params.RefreshToken)
	if err != nil {
		if errors.Is(err, service.ErrRefreshTokenExpired) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return err
	}

	return c.Status(fiber.StatusOK).JSON(credentials)
}

func (controller *Controller) SignUp(c fiber.Ctx) error {
	if controller.disableDefaultSignUp {
		return c.Status(fiber.StatusNotFound).SendString("Cannot POST /api/v1/auth/sign-up")
	}

	var params apigen.SignUpRequest
	if err := c.Bind().Body(&params); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	exists, err := controller.svc.IsUsernameExists(c.Context(), params.Name)
	if err != nil {
		return err
	}
	if exists {
		return c.SendStatus(fiber.StatusConflict)
	}

	userMeta, err := controller.svc.CreateNewUser(c.Context(), params.Name, params.Password)
	if err != nil {
		return err
	}

	credentials, err := controller.svc.SignIn(c.Context(), userMeta.UserID)
	if err != nil {
		return err
	}

	return c.Status(fiber.StatusCreated).JSON(credentials)
}

func (controller *Controller) ListTasks(c fiber.Ctx) error {
	ret, err := controller.svc.ListTasks(c.Context())
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusOK).JSON(ret)
}

func (controller *Controller) ListEvents(c fiber.Ctx) error {
	ret, err := controller.svc.ListEvents(c.Context())
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusOK).JSON(ret)
}

func (controller *Controller) ListOrgs(c fiber.Ctx) error {
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

func (controller *Controller) TryExecuteTask(c fiber.Ctx, taskID int32) error {
	if !controller.enableWorkerHTTPTrigger {
		return c.Status(fiber.StatusNotFound).SendString("Cannot GET /api/v1/tasks/try-execute")
	}

	err := controller.svc.TryExecuteTask(c.Context(), taskID)
	if err != nil {
		return err
	}

	return c.Status(fiber.StatusOK).SendString("Task executed")
}
