package service

import (
	"context"
	"time"

	"github.com/cloudcarver/anchor/pkg/auth"
	"github.com/cloudcarver/anchor/pkg/config"
	"github.com/cloudcarver/anchor/pkg/hooks"
	"github.com/cloudcarver/anchor/pkg/utils"
	"github.com/cloudcarver/anchor/pkg/zcore/model"
	"github.com/cloudcarver/anchor/pkg/zgen/apigen"
	"github.com/pkg/errors"
)

type (
	TradeType   string
	TradeStatus string
	DdWorkEvent string
)

var (
	ErrUserNotFound                  = errors.New("user not found")
	ErrInvalidPassword               = errors.New("invalid password")
	ErrRefreshTokenExpired           = errors.New("refresh token expired")
	ErrDatabaseNotFound              = errors.New("database not found")
	ErrClusterNotFound               = errors.New("cluster not found")
	ErrClusterHasDatabaseConnections = errors.New("cluster has database connections")
	ErrDiagnosticNotFound            = errors.New("diagnostic not found")
)

const (
	ExpireDuration             = 2 * time.Minute
	DefaultMaxRetries          = 3
	RefreshTokenExpireDuration = 14 * 24 * time.Hour
)

type ServiceInterface interface {
	// Create a new user and its default organization
	CreateNewUser(ctx context.Context, username, password string) (int32, error)

	// IsUsernameExists returns true if the username exists
	IsUsernameExists(ctx context.Context, username string) (bool, error)

	DeleteUserByName(ctx context.Context, username string) error

	RestoreUserByName(ctx context.Context, username string) error

	CreateTestAccount(ctx context.Context, username, password string) (int32, error)

	// SignIn authenticates a user and returns credentials
	SignIn(ctx context.Context, params apigen.SignInRequest) (*apigen.Credentials, error)

	RefreshToken(ctx context.Context, userID int32, refreshToken string) (*apigen.Credentials, error)

	ListTasks(ctx context.Context) ([]apigen.Task, error)

	GetTaskByID(ctx context.Context, id int32) (*apigen.Task, error)

	ListEvents(ctx context.Context) ([]apigen.Event, error)

	ListOrgs(ctx context.Context, userID int32) ([]apigen.Org, error)

	UpdateUserPassword(ctx context.Context, username, password string) (int32, error)
}

type Service struct {
	m     model.ModelInterface
	auth  auth.AuthInterface
	hooks hooks.AnchorHookInterface

	generateSaltAndHash func(password string) (string, string, error)
	now                 func() time.Time
}

func NewService(
	cfg *config.Config,
	m model.ModelInterface,
	auth auth.AuthInterface,
	hooks hooks.AnchorHookInterface,
) ServiceInterface {
	return &Service{
		m:                   m,
		auth:                auth,
		hooks:               hooks,
		now:                 time.Now,
		generateSaltAndHash: utils.GenerateSaltAndHash,
	}
}
