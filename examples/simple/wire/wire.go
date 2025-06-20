//go:build wireinject
// +build wireinject

package wire

import (
	"myexampleapp/pkg"
	"myexampleapp/pkg/asynctask"
	"myexampleapp/pkg/config"
	"myexampleapp/pkg/handler"
	"myexampleapp/pkg/zcore/injection"
	"myexampleapp/pkg/zcore/model"
	"myexampleapp/pkg/zgen/taskgen"

	anchor_config "github.com/cloudcarver/anchor/pkg/config"
	"github.com/google/wire"
)

func InitApp() (*pkg.App, error) {
	wire.Build(
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
		pkg.NewAnchorApp,
		pkg.NewApp,
		pkg.NewPlugin,
		anchor_config.DefaultLibConfig,
	)
	return nil, nil
}
