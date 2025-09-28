//go:build wireinject
// +build wireinject

package wire

import (
	"github.com/cloudcarver/anclax/pkg/app"
	"github.com/cloudcarver/anclax/pkg/asynctask"
	"github.com/cloudcarver/anclax/pkg/auth"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/controller"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/hooks"
	"github.com/cloudcarver/anclax/pkg/macaroons"
	"github.com/cloudcarver/anclax/pkg/macaroons/store"
	"github.com/cloudcarver/anclax/pkg/metrics"
	"github.com/cloudcarver/anclax/pkg/server"
	"github.com/cloudcarver/anclax/pkg/service"
	"github.com/cloudcarver/anclax/pkg/taskcore"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/ws"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
	"github.com/google/wire"
)

func InitializeApplication(cfg *config.Config, libCfg *config.LibConfig) (*app.Application, error) {
	wire.Build(
		app.NewDebugServer,
		app.NewApplication,
		app.NewCloserManager,
		service.NewService,
		controller.NewController,
		controller.NewValidator,
		model.NewModel,
		server.NewServer,
		auth.NewAuth,
		macaroons.NewMacaroonManager,
		store.NewStore,
		taskcore.NewTaskStore,
		macaroons.NewCaveatParser,
		globalctx.New,
		metrics.NewMetricsServer,
		worker.NewWorker,
		taskgen.NewTaskHandler,
		taskgen.NewTaskRunner,
		asynctask.NewExecutor,
		hooks.NewBaseHook,
		ws.NewWebsocketController,
	)
	return nil, nil
}
