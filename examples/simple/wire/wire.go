//go:build wireinject
// +build wireinject

package wire

import (
	"myexampleapp/internal/asynctask"
	"myexampleapp/internal/config"
	"myexampleapp/internal/handler"
	"myexampleapp/internal/zcore/initapp"
	"myexampleapp/internal/zcore/injection"
	"myexampleapp/internal/zcore/model"
	"myexampleapp/internal/zgen/taskgen"

	anchor_wire "github.com/cloudcarver/anchor/wire"

	"github.com/google/wire"
)

func InitApp() (*initapp.App, error) {
	wire.Build(
		anchor_wire.InitializeApplication,
		initapp.NewApp,
		injection.InjectAuth,
		injection.InjectTaskStore,
		injection.InjectAnchorSvc,
		handler.NewHandler,
		handler.NewValidator,
		taskgen.NewTaskHandler,
		taskgen.NewTaskRunner,
		asynctask.NewExecutor,
		model.NewModel,
		config.NewConfig,
	)
	return nil, nil
}
