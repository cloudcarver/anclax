package service

import (
	"context"
	"time"

	"github.com/cloudcarver/anclax/core"
	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/hooks"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/apigen"
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
	CreateNewUser(ctx context.Context, username, password string) (*UserMeta, error)

	CreateNewUserWithTx(ctx context.Context, tx core.Tx, username, password string) (*UserMeta, error)

	GetUserByUserName(ctx context.Context, username string) (*UserMeta, error)

	// IsUsernameExists returns true if the username exists
	IsUsernameExists(ctx context.Context, username string) (bool, error)

	DeleteUserByName(ctx context.Context, username string) error

	RestoreUserByName(ctx context.Context, username string) error

	CreateTestAccount(ctx context.Context, username, password string) (int32, error)

	// SignIn authenticates a user and returns credentials
	SignIn(ctx context.Context, userID int32) (*apigen.Credentials, error)

	SignInWithPassword(ctx context.Context, params apigen.SignInRequest) (*apigen.Credentials, error)

	RefreshToken(ctx context.Context, refreshToken string) (*apigen.Credentials, error)

	ListTasks(ctx context.Context) ([]apigen.Task, error)

	GetTaskByID(ctx context.Context, id int32) (*apigen.Task, error)

	ListEvents(ctx context.Context) ([]apigen.Event, error)

	ListOrgs(ctx context.Context, userID int32) ([]apigen.Org, error)

	UpdateUserPassword(ctx context.Context, username, password string) (int32, error)

	TryExecuteTask(ctx context.Context, taskID int32) error
}

type Service struct {
	m      model.ModelInterface
	auth   auth.AuthInterface
	hooks  hooks.AnclaxHookInterface
	worker worker.WorkerInterface

	singleSession bool

	generateSaltAndHash func(password string) (string, string, error)
	now                 func() time.Time
}

func NewService(
	cfg *config.Config,
	m model.ModelInterface,
	auth auth.AuthInterface,
	hooks hooks.AnclaxHookInterface,
) ServiceInterface {
	return &Service{
		m:                   m,
		auth:                auth,
		hooks:               hooks,
		now:                 time.Now,
		generateSaltAndHash: utils.GenerateSaltAndHash,
		singleSession:       cfg.Auth.SingleSession,
	}
}
