package app

import (
	"context"

	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/hooks"
	"github.com/cloudcarver/anclax/pkg/macaroons"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/server"
	"github.com/cloudcarver/anclax/pkg/service"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

type PluginMeta struct {
	Namespace string `json:"name"`
	// Add other fields as necessary
}

type Application struct {
	server        *server.Server
	prometheus    *metrics.MetricsServer
	worker        worker.WorkerInterface
	disableWorker bool
	debugServer   *DebugServer
	auth          auth.AuthInterface
	taskStore     taskcore.TaskStoreInterface
	service       service.ServiceInterface
	hooks         hooks.AnclaxHookInterface
	caveatParser  macaroons.CaveatParserInterface
	globalctx     *globalctx.GlobalContext
	cm            *closer.CloserManager
}

func NewApplication(
	globalctx *globalctx.GlobalContext,
	cfg *config.Config,
	server *server.Server,
	prometheus *metrics.MetricsServer,
	worker worker.WorkerInterface,
	debugServer *DebugServer,
	auth auth.AuthInterface,
	taskStore taskcore.TaskStoreInterface,
	service service.ServiceInterface,
	hooks hooks.AnclaxHookInterface,
	caveatParser macaroons.CaveatParserInterface,
	cm *closer.CloserManager,
) (*Application, error) {

	if cfg.TestAccount != nil {
		if _, err := service.CreateTestAccount(context.TODO(), "test", cfg.TestAccount.Password); err != nil {
			return nil, errors.Wrapf(err, "failed to create test account")
		}
	}

	app := &Application{
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
		globalctx:     globalctx,
		cm:            cm,
	}

	return app, nil
}

func (a *Application) Start() error {
	defer func() {
		if r := recover(); r != nil {
			log.Error("application received panic, shutting down", zap.Any("panic", r))
			a.Close()
			panic(r)
		}
	}()

	go a.debugServer.Start()
	go a.prometheus.Start()
	if !a.disableWorker {
		go a.worker.Start()
	}
	return a.server.Listen()
}

func (a *Application) GetCloserManager() *closer.CloserManager {
	return a.cm
}

func (a *Application) Close() {
	a.cm.Close()
}

func (a *Application) GetServer() *server.Server {
	return a.server
}

func (a *Application) GetWorker() worker.WorkerInterface {
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

func (a *Application) GetHooks() hooks.AnclaxHookInterface {
	return a.hooks
}

func (a *Application) GetCaveatParser() macaroons.CaveatParserInterface {
	return a.caveatParser
}

func (a *Application) GetGlobalCtx() *globalctx.GlobalContext {
	return a.globalctx
}

func (a *Application) Plug(plugins ...Plugin) error {
	for _, plugin := range plugins {
		if err := plugin.PlugTo(a); err != nil {
			return errors.Wrapf(err, "failed to plug plugin %T", plugin)
		}
	}
	return nil
}

type Plugin interface {
	PlugTo(app *Application) error
}
