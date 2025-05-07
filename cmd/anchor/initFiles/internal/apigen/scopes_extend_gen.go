package apigen

import "github.com/gofiber/fiber/v2"

type Validator interface { 
    PreValidate(*fiber.Ctx) error
    
    PostValidate(*fiber.Ctx) error
 }


type XMiddleware struct {
	Handler ServerInterface
	Validator
}

func NewXMiddleware(handler ServerInterface, validator Validator) ServerInterface {
	return &XMiddleware{Handler: handler, Validator: validator}
}

// Get Counter
// (GET /counter)
func (x *XMiddleware) GetCounter(c *fiber.Ctx) error {
    
	if err := x.PreValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
	
	
	if err := x.PostValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
    return x.Handler.GetCounter(c)
}

