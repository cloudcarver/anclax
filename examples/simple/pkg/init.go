package pkg

import (
	"context"
	"myexampleapp/pkg/config"
	"myexampleapp/pkg/zgen/apigen"
	"myexampleapp/pkg/zgen/taskgen"

	anchor_wire "github.com/cloudcarver/anchor/wire"

	"github.com/cloudcarver/anchor/pkg/app"
	anchor_app "github.com/cloudcarver/anchor/pkg/app"
	anchor_svc "github.com/cloudcarver/anchor/pkg/service"
	"github.com/cloudcarver/anchor/pkg/taskcore"
	"github.com/cloudcarver/anchor/pkg/taskcore/worker"
	"github.com/gofiber/fiber/v2"
)

type App struct {
	anchorApp *anchor_app.Application
}

func (a *App) Start() error {
	return a.anchorApp.Start()
}

func NewAnchorApp(cfg *config.Config) (*anchor_app.Application, error) {
	cfg.Anchor.Pg.DSN = cfg.Pg.DSN
	cfg.Anchor.Host = cfg.Host

	anchorApp, err := anchor_wire.InitializeApplication(&cfg.Anchor)
	if err != nil {
		return nil, err
	}

	return anchorApp, nil
}

func NewApp(anchorApp *anchor_app.Application, anchorSvc anchor_svc.ServiceInterface, taskrunner taskgen.TaskRunner, plugin *Plugin) (*App, error) {
	// register task handler
	plugin.Plug(anchorApp)

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

type Plugin struct {
	serverInterface apigen.ServerInterface
	validator       apigen.Validator
	taskHandler     worker.TaskHandler
}

func NewPlugin(serverInterface apigen.ServerInterface, validator apigen.Validator, taskHandler worker.TaskHandler) *Plugin {
	return &Plugin{
		serverInterface: serverInterface,
		validator:       validator,
		taskHandler:     taskHandler,
	}
}

func (p *Plugin) Plug(anchorApp *app.Application) {
	p.PlugToFiberApp(anchorApp.GetServer().GetApp())
	p.PlugToWorker(anchorApp.GetWorker())
}

func (p *Plugin) PlugToFiberApp(fiberApp *fiber.App) {
	apigen.RegisterHandlersWithOptions(fiberApp, apigen.NewXMiddleware(p.serverInterface, p.validator), apigen.FiberServerOptions{
		BaseURL:     "/api/v1",
		Middlewares: []apigen.MiddlewareFunc{},
	})
}

func (p *Plugin) PlugToWorker(worker *worker.Worker) {
	worker.RegisterTaskHandler(p.taskHandler)
}
