package handler

import (
	"github.com/cloudcarver/anchor/example-app/internal/apigen"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
}

func NewHandler() apigen.ServerInterface {
	return &Handler{}
}

func (h *Handler) GetCounter(c *fiber.Ctx) error {
	return c.JSON(apigen.Counter{Count: 0})
}
