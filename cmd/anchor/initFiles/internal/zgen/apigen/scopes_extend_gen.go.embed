package apigen

import "github.com/gofiber/fiber/v2"

type Validator interface { 
    PreValidate(*fiber.Ctx) error
    
    PostValidate(*fiber.Ctx) error

    OperationPermit(c *fiber.Ctx, operationID string) error
 }


type XMiddleware struct {
	ServerInterface
	Validator
}

func NewXMiddleware(handler ServerInterface, validator Validator) ServerInterface {
	return &XMiddleware{ServerInterface: handler, Validator: validator}
}

// Increment Counter
// (POST /counter)
func (x *XMiddleware) IncrementCounter(c *fiber.Ctx) error {
    if c.Get("Authorization") == "" {
		return c.Status(fiber.StatusUnauthorized).SendString("Authorization header is required")
	} 
	if err := x.PreValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
	operationID := "IncrementCounter"  
	if err := x.OperationPermit(c, operationID); err != nil {
	    return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}  
	if err := x.PostValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
    return x.ServerInterface.IncrementCounter(c)
}

