//go:build wireinject
// +build wireinject

package wire

import (
	"github.com/cloudcarver/anchor/internal/app"
	"github.com/cloudcarver/anchor/internal/auth"
	"github.com/cloudcarver/anchor/internal/config"
	"github.com/cloudcarver/anchor/internal/controller"
	"github.com/cloudcarver/anchor/internal/globalctx"
	"github.com/cloudcarver/anchor/internal/macaroons"
	"github.com/cloudcarver/anchor/internal/macaroons/store"
	"github.com/cloudcarver/anchor/internal/metrics"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/server"
	"github.com/cloudcarver/anchor/internal/service"
	"github.com/cloudcarver/anchor/internal/task"
	"github.com/cloudcarver/anchor/internal/task/runner"
	"github.com/cloudcarver/anchor/internal/task/worker"
	"github.com/google/wire"
)

func InitializeApplication() (*app.Application, error) {
	wire.Build(
		app.NewDebugServer,
		app.NewApplication,
		config.NewConfig,
		service.NewService,
		controller.NewController,
		controller.NewValidator,
		model.NewModel,
		server.NewServer,
		auth.NewAuth,
		macaroons.NewMacaroonManager,
		store.NewStore,
		task.NewTaskStore,
		auth.NewCaveatParser,
		globalctx.New,
		metrics.NewMetricsServer,
		worker.NewWorker,
		runner.NewTaskHandler,
		runner.NewTaskRunner,
		runner.NewExecutor,
	)
	return nil, nil
}
