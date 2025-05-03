package service

import (
	"context"
	"time"

	"github.com/cloudcarver/anchor/internal/apigen"
	"github.com/cloudcarver/anchor/internal/auth"
	"github.com/cloudcarver/anchor/internal/config"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/utils"
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

	// SignIn authenticates a user and returns credentials
	SignIn(ctx context.Context, params apigen.SignInRequest) (*apigen.Credentials, error)

	RefreshToken(ctx context.Context, userID int32, refreshToken string) (*apigen.Credentials, error)

	ListTasks(ctx context.Context) ([]apigen.Task, error)

	ListEvents(ctx context.Context) ([]apigen.Event, error)
}

type Service struct {
	m    model.ModelInterface
	auth auth.AuthInterface

	generateHashAndSalt func(password string) (string, string, error)
	now                 func() time.Time
}

func NewService(
	cfg *config.Config,
	m model.ModelInterface,
	auth auth.AuthInterface,
) ServiceInterface {
	return &Service{
		m:                   m,
		auth:                auth,
		now:                 time.Now,
		generateHashAndSalt: utils.GenerateHashAndSalt,
	}
}
