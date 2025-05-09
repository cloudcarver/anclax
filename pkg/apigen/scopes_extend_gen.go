package apigen

import "github.com/gofiber/fiber/v2"

type Validator interface { 
    PreValidate(*fiber.Ctx) error
    
    PostValidate(*fiber.Ctx) error
 
    GetOrgID(c *fiber.Ctx) int32
}


type XMiddleware struct {
	Handler ServerInterface
	Validator
}

func NewXMiddleware(handler ServerInterface, validator Validator) ServerInterface {
	return &XMiddleware{Handler: handler, Validator: validator}
}

// Refresh access token
// (POST /auth/refresh)
func (x *XMiddleware) RefreshToken(c *fiber.Ctx) error {
    
	if err := x.PreValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
	 
	if err := x.PostValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
    return x.Handler.RefreshToken(c)
}
// Sign in user
// (POST /auth/sign-in)
func (x *XMiddleware) SignIn(c *fiber.Ctx) error {
    
	if err := x.PreValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
	 
	if err := x.PostValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
    return x.Handler.SignIn(c)
}
// Sign out user
// (POST /auth/sign-out)
func (x *XMiddleware) SignOut(c *fiber.Ctx) error {
    if c.Get("Authorization") == "" {
		return c.Status(fiber.StatusUnauthorized).SendString("Authorization header is required")
	} 
	if err := x.PreValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
	   
	if err := x.PostValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
    return x.Handler.SignOut(c)
}
// Get all events
// (GET /events)
func (x *XMiddleware) ListEvents(c *fiber.Ctx) error {
    if c.Get("Authorization") == "" {
		return c.Status(fiber.StatusUnauthorized).SendString("Authorization header is required")
	} 
	if err := x.PreValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
	   
	if err := x.PostValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
    return x.Handler.ListEvents(c)
}
// Get all tasks
// (GET /tasks)
func (x *XMiddleware) ListTasks(c *fiber.Ctx) error {
    if c.Get("Authorization") == "" {
		return c.Status(fiber.StatusUnauthorized).SendString("Authorization header is required")
	} 
	if err := x.PreValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
	   
	if err := x.PostValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
    return x.Handler.ListTasks(c)
}

