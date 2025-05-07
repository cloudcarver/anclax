package initapp

import (
	"github.com/cloudcarver/anchor/example-app/internal/apigen"
	"github.com/cloudcarver/anchor/pkg/app"
	"github.com/cloudcarver/anchor/pkg/task/worker"
)

type App struct {
	anchorApp *app.Application
}

func NewApp(anchorApp *app.Application, serverInterface apigen.ServerInterface, validator apigen.Validator, taskHandler worker.TaskHandler) *App {
	fiberApp := anchorApp.GetServer().GetApp()

	apigen.RegisterHandlersWithOptions(fiberApp, apigen.NewXMiddleware(serverInterface, validator), apigen.FiberServerOptions{
		BaseURL:     "/api/v1",
		Middlewares: []apigen.MiddlewareFunc{},
	})

	anchorApp.GetWorker().RegisterTaskHandler(taskHandler)

	return &App{
		anchorApp: anchorApp,
	}
}

func (a *App) Start() error {
	return a.anchorApp.Start()
}
