package apigen

import "github.com/gofiber/fiber/v2"

type Validator interface { 
    PreValidate(*fiber.Ctx) error
    
    PostValidate(*fiber.Ctx) error
 
    GetOrgID(c *fiber.Ctx) int32
}


type XMiddleware struct {
	ServerInterface
	Validator
}

func NewXMiddleware(handler ServerInterface, validator Validator) ServerInterface {
	return &XMiddleware{ServerInterface: handler, Validator: validator}
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
    return x.ServerInterface.SignOut(c)
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
    return x.ServerInterface.ListEvents(c)
}
// Get all organizations of which the user is a member
// (GET /orgs)
func (x *XMiddleware) ListOrgs(c *fiber.Ctx) error {
    if c.Get("Authorization") == "" {
		return c.Status(fiber.StatusUnauthorized).SendString("Authorization header is required")
	} 
	if err := x.PreValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
	   
	if err := x.PostValidate(c); err != nil {
		return c.Status(fiber.StatusForbidden).SendString(err.Error())
	}
    return x.ServerInterface.ListOrgs(c)
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
    return x.ServerInterface.ListTasks(c)
}

