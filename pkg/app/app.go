package app

import (
	"context"

	"github.com/cloudcarver/anchor/pkg/auth"
	"github.com/cloudcarver/anchor/pkg/config"
	"github.com/cloudcarver/anchor/pkg/hooks"
	"github.com/cloudcarver/anchor/pkg/macaroons"
	"github.com/cloudcarver/anchor/pkg/metrics"
	"github.com/cloudcarver/anchor/pkg/server"
	"github.com/cloudcarver/anchor/pkg/service"
	"github.com/cloudcarver/anchor/pkg/taskcore"
	"github.com/cloudcarver/anchor/pkg/taskcore/worker"
	"github.com/pkg/errors"
)

type Application struct {
	server        *server.Server
	prometheus    *metrics.MetricsServer
	worker        *worker.Worker
	disableWorker bool
	debugServer   *DebugServer
	auth          auth.AuthInterface
	taskStore     taskcore.TaskStoreInterface
	service       service.ServiceInterface
	hooks         hooks.AnchorHookInterface
	caveatParser  macaroons.CaveatParserInterface
}

func NewApplication(
	cfg *config.Config,
	server *server.Server,
	prometheus *metrics.MetricsServer,
	worker *worker.Worker,
	debugServer *DebugServer,
	auth auth.AuthInterface,
	taskStore taskcore.TaskStoreInterface,
	service service.ServiceInterface,
	hooks hooks.AnchorHookInterface,
	caveatParser macaroons.CaveatParserInterface,
) (*Application, error) {

	if cfg.TestAccount != nil {
		if _, err := service.CreateTestAccount(context.TODO(), "test", cfg.TestAccount.Password); err != nil {
			return nil, errors.Wrapf(err, "failed to create test account")
		}
	}

	return &Application{
		server:        server,
		prometheus:    prometheus,
		worker:        worker,
		disableWorker: cfg.Worker.Disable,
		debugServer:   debugServer,
		auth:          auth,
		taskStore:     taskStore,
		service:       service,
		hooks:         hooks,
		caveatParser:  caveatParser,
	}, nil
}

func (a *Application) Start() error {
	go a.debugServer.Start()
	go a.prometheus.Start()
	if !a.disableWorker {
		go a.worker.Start()
	}
	return a.server.Listen()
}

func (a *Application) GetServer() *server.Server {
	return a.server
}

func (a *Application) GetWorker() *worker.Worker {
	return a.worker
}

func (a *Application) GetAuth() auth.AuthInterface {
	return a.auth
}

func (a *Application) GetTaskStore() taskcore.TaskStoreInterface {
	return a.taskStore
}

func (a *Application) GetService() service.ServiceInterface {
	return a.service
}

func (a *Application) GetHooks() hooks.AnchorHookInterface {
	return a.hooks
}

func (a *Application) GetCaveatParser() macaroons.CaveatParserInterface {
	return a.caveatParser
}
