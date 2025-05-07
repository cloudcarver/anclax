package macaroons

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
)

type CaveatParser interface {
	Parse(string) (Caveat, error)
}

type Caveat interface {
	Encode() (string, error)

	Decode(string) error

	Type() string

	Validate(*fiber.Ctx) error
}

type MacaroonManagerInterface interface {
	CreateToken(ctx context.Context, userID int32, caveats []Caveat, ttl time.Duration) (*Macaroon, error)

	Parse(ctx context.Context, token string) (*Macaroon, error)

	InvalidateUserTokens(ctx context.Context, userID int32) error
}
