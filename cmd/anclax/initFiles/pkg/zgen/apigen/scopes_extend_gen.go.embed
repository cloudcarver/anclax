package apigen

import "github.com/gofiber/fiber/v2"

type Validator interface { 
    // AuthFunc is called before the request is processed. The response will be 401 if the auth fails.
    AuthFunc(*fiber.Ctx) error

    // PreValidate is called before the request is processed. The response will be 403 if the validation fails.
    PreValidate(*fiber.Ctx) error
    
    // PostValidate is called after the request is processed. The response will be 403 if the validation fails.
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
    if err := x.AuthFunc(c); err != nil {
		return c.Status(fiber.StatusUnauthorized).SendString(err.Error())
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

