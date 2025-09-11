package utils

import (
	"fmt"

	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("fiber")

func ErrorHandler(c *fiber.Ctx, err error) error {
	// default 500
	var code = fiber.StatusInternalServerError

	// Retrieve the custom status code if it's a *fiber.Error
	var e *fiber.Error
	if errors.As(err, &e) {
		code = e.Code
	}

	// Set Content-Type: text/plain; charset=utf-8
	c.Set(fiber.HeaderContentType, fiber.MIMETextPlainCharsetUTF8)

	rid := c.Locals(requestid.ConfigDefault.ContextKey)

	if code == fiber.StatusInternalServerError {
		log.Info(fmt.Sprintf("unexpected error, request-id: %v, err: %v", rid, err), zap.Error(err), zap.String("path", c.Path()))
		return c.Status(code).SendString(fmt.Sprintf("unexpected error, request-id: %v", rid))
	}

	// Return status code with error message
	return c.Status(code).SendString(err.Error())
}
