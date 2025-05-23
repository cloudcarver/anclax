package pkg

import (
	"context"
	"myexampleapp/pkg/zgen/apigen"
	"myexampleapp/pkg/zgen/taskgen"

	anchor_app "github.com/cloudcarver/anchor/pkg/app"
	anchor_svc "github.com/cloudcarver/anchor/pkg/service"
	"github.com/cloudcarver/anchor/pkg/taskcore"
	"github.com/cloudcarver/anchor/pkg/taskcore/worker"
)

type App struct {
	anchorApp *anchor_app.Application
}

func (a *App) Start() error {
	return a.anchorApp.Start()
}

func NewApp(anchorApp *anchor_app.Application, anchorSvc anchor_svc.ServiceInterface, taskrunner taskgen.TaskRunner, taskHandler worker.TaskHandler, serverInterface apigen.ServerInterface, validator apigen.Validator) (*App, error) {
	// register task handler
	anchorApp.GetWorker().RegisterTaskHandler(taskHandler)

	// register api handler
	apigen.RegisterHandlersWithOptions(anchorApp.GetServer().GetApp(), apigen.NewXMiddleware(serverInterface, validator), apigen.FiberServerOptions{
		BaseURL:     "/api/v1",
		Middlewares: []apigen.MiddlewareFunc{},
	})

	// other init steps
	if _, err := anchorSvc.CreateNewUser(context.Background(), "test", "test"); err != nil {
		return nil, err
	}
	if _, err := taskrunner.RunAutoIncrementCounter(context.Background(), &taskgen.AutoIncrementCounterParameters{
		Amount: 1,
	}, taskcore.WithUniqueTag("auto-increment-counter")); err != nil {
		return nil, err
	}

	return &App{
		anchorApp: anchorApp,
	}, nil
}
