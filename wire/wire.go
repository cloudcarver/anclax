//go:build wireinject
// +build wireinject

package wire

import (
	"github.com/cloudcarver/anchor/pkg/app"
	"github.com/cloudcarver/anchor/pkg/auth"
	"github.com/cloudcarver/anchor/pkg/config"
	"github.com/cloudcarver/anchor/pkg/controller"
	"github.com/cloudcarver/anchor/pkg/globalctx"
	"github.com/cloudcarver/anchor/pkg/macaroons"
	"github.com/cloudcarver/anchor/pkg/macaroons/store"
	"github.com/cloudcarver/anchor/pkg/metrics"
	"github.com/cloudcarver/anchor/pkg/model"
	"github.com/cloudcarver/anchor/pkg/server"
	"github.com/cloudcarver/anchor/pkg/service"
	"github.com/cloudcarver/anchor/pkg/task"
	"github.com/cloudcarver/anchor/pkg/task/runner"
	"github.com/cloudcarver/anchor/pkg/task/worker"
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
