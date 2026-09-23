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

// CaveatSettings defines the policy for a caveat type.
type CaveatSettings struct {
	// AllowDuplicates permits multiple caveats with the same Type in one token.
	// The zero value rejects duplicates, even when their values are identical.
	// When enabled, every occurrence must still pass Validate.
	AllowDuplicates bool
}

type Caveat interface {
	Type() string

	// Settings returns the policy for this caveat type. It must not depend on
	// values decoded from the token.
	Settings() CaveatSettings

	Validate(fiber.Ctx) error
}

type MacaroonManagerInterface interface {
	CreateToken(ctx context.Context, caveats []Caveat, ttl time.Duration, group string) (*Macaroon, error)

	Parse(ctx context.Context, token string) (*Macaroon, error)

	// InvalidateTokensByGroup invalidates all tokens in the given group.
	InvalidateTokensByGroup(ctx context.Context, group string) error

	InvalidateToken(ctx context.Context, keyID int64) error
}
