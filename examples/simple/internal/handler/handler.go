package handler

import (
	"context"
	"myexampleapp/internal/zcore/model"
	"myexampleapp/internal/zgen/apigen"
	"myexampleapp/internal/zgen/taskgen"

	anchor_svc "github.com/cloudcarver/anchor/pkg/service"
	"github.com/cloudcarver/anchor/pkg/taskcore"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	model      model.ModelInterface
	taskrunner taskgen.TaskRunner
}

func NewHandler(model model.ModelInterface, taskrunner taskgen.TaskRunner, anchorSvc anchor_svc.ServiceInterface) (apigen.ServerInterface, error) {
	if _, err := anchorSvc.CreateNewUser(context.Background(), "test", "test"); err != nil {
		return nil, err
	}
	if _, err := taskrunner.RunAutoIncrementCounter(context.Background(), &taskgen.AutoIncrementCounterParameters{
		Amount: 1,
	}, taskcore.WithUniqueTag("auto-increment-counter")); err != nil {
		return nil, err
	}

	return &Handler{model, taskrunner}, nil
}

func (h *Handler) GetCounter(c *fiber.Ctx) error {
	count, err := h.model.GetCounter(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	return c.JSON(apigen.Counter{Count: count.Value})
}

func (h *Handler) IncrementCounter(c *fiber.Ctx) error {
	_, err := h.taskrunner.RunIncrementCounter(c.Context(), &taskgen.IncrementCounterParameters{
		Amount: 1,
	})
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).SendString("Incremented")
}
