package handler

import (
	"myexampleapp/pkg/model"
	"myexampleapp/pkg/zgen/apigen"
	counter "myexampleapp/pkg/zgen/schemas/counter"
	"myexampleapp/pkg/zgen/taskgen"

	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	model      model.ModelInterface
	taskrunner taskgen.TaskRunner
}

func NewHandler(model model.ModelInterface, taskrunner taskgen.TaskRunner) (apigen.ServerInterface, error) {
	return &Handler{model, taskrunner}, nil
}

func (h *Handler) GetCounter(c *fiber.Ctx) error {
	count, err := h.model.GetCounter(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}
	return c.JSON(counter.Counter{Count: count.Value})
}

func (h *Handler) IncrementCounter(c *fiber.Ctx) error {
	_, err := h.taskrunner.RunIncrementCounter(c.Context(), &counter.IncrementCounterParams{
		Amount: 1,
	})
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).SendString("Incremented")
}
