package macaroons

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v3"
)

type CaveatParserInterface interface {
	// Parse parses the given caveat string and returns the caveat
	Parse(string) (Caveat, error)

	// Register registers a new caveat constructor for the given type
	Register(typ string, constructor CaveatConstructor) error
}

type Caveat interface {
	Type() string

	Validate(fiber.Ctx) error
}

type MacaroonManagerInterface interface {
	CreateToken(ctx context.Context, caveats []Caveat, ttl time.Duration, group string) (*Macaroon, error)

	Parse(ctx context.Context, token string) (*Macaroon, error)

	// InvalidateTokensByGroup invalidates all tokens in the given group.
	InvalidateTokensByGroup(ctx context.Context, group string) error

	InvalidateToken(ctx context.Context, keyID int64) error
}
