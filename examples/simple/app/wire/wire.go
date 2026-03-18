//go:build wireinject
// +build wireinject

package wire

import (
	"myexampleapp/app"
	"myexampleapp/pkg/asynctask"
	"myexampleapp/pkg/config"
	"myexampleapp/pkg/handler"
	"myexampleapp/pkg/model"
	"myexampleapp/pkg/zgen/taskgen"

	"github.com/google/wire"
)

func InitApp() (*app.App, error) {
	wire.Build(
		app.InjectAuth,
		app.InjectTaskStore,
		handler.NewHandler,
		handler.NewValidator,
		taskgen.NewTaskHandler,
		taskgen.NewTaskRunner,
		asynctask.NewExecutor,
		model.NewModel,
		config.NewConfig,
		app.Init,
		app.InitAnclaxApplication,
		app.NewPlugin,
	)
	return nil, nil
}
