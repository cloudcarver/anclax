package auth

import (
	"strings"

	macaroons "github.com/cloudcarver/anchor/pkg/macaroons"
	"github.com/gofiber/fiber/v2"
	"github.com/pkg/errors"
)

const (
	CaveatUserContext = "user_context"
	CaveatRefreshOnly = "refresh_only"
)

type UserContextCaveat struct {
	Typ    string `json:"type"`
	UserID int32  `json:"user_id"`
	OrgID  int32  `json:"org_id"`
}

func NewUserContextCaveat(userID int32, orgID int32) *UserContextCaveat {
	return &UserContextCaveat{
		Typ:    CaveatUserContext,
		UserID: userID,
		OrgID:  orgID,
	}
}

func (uc *UserContextCaveat) Type() string {
	return uc.Typ
}

func (uc *UserContextCaveat) Validate(ctx *fiber.Ctx) error {
	ctx.Locals(ContextKeyUserID, uc.UserID)
	ctx.Locals(ContextKeyOrgID, uc.OrgID)
	return nil
}

type RefreshOnlyCaveat struct {
	Typ         string `json:"type"`
	UserID      int32  `json:"user_id"`
	AccessKeyID int64  `json:"access_key_id"`
}

func NewRefreshOnlyCaveat(userID int32, accessKeyID int64) *RefreshOnlyCaveat {
	return &RefreshOnlyCaveat{
		Typ:         CaveatRefreshOnly,
		UserID:      userID,
		AccessKeyID: accessKeyID,
	}
}

func (rc *RefreshOnlyCaveat) Type() string {
	return rc.Typ
}

func (rc *RefreshOnlyCaveat) Validate(ctx *fiber.Ctx) error {
	if ctx.Method() == "POST" && strings.HasSuffix(ctx.Path(), "/auth/refresh") {
		return nil
	}
	return errors.Wrapf(macaroons.ErrCaveatCheckFailed, "invalid request: %s %s, the token is for refresh only", ctx.Method(), ctx.Path())
}
