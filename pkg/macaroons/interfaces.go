package macaroons

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
)

type CaveatParserInterface interface {
	// Parse parses the given caveat string and returns the caveat
	Parse(string) (Caveat, error)

	// Register registers a new caveat constructor for the given type
	Register(typ string, constructor CaveatConstructor) error
}

type Caveat interface {
	Type() string

	Validate(*fiber.Ctx) error
}

type MacaroonParserInterface interface {
	CreateToken(ctx context.Context, userID int32, caveats []Caveat, ttl time.Duration) (*Macaroon, error)

	Parse(ctx context.Context, token string) (*Macaroon, error)

	InvalidateUserTokens(ctx context.Context, userID int32) error
}
