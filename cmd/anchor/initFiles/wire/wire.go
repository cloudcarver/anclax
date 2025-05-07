//go:build wireinject
// +build wireinject

package wire

import (
	"github.com/cloudcarver/anchor/example-app/internal/handler"
	"github.com/cloudcarver/anchor/example-app/internal/initapp"
	"github.com/cloudcarver/anchor/example-app/internal/task"
	anchor_wire "github.com/cloudcarver/anchor/wire"
	"github.com/google/wire"
)

func InitApp() (*initapp.App, error) {
	wire.Build(
		anchor_wire.InitializeApplication,
		initapp.NewApp,
		handler.NewHandler,
		handler.NewValidator,
		task.NewTaskHandler,
		task.NewExecutor,
	)
	return nil, nil
}
