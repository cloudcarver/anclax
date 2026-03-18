package app

import (
	"context"
	"myexampleapp/pkg/config"
	"myexampleapp/pkg/model"
	"myexampleapp/pkg/zgen/apigen"
	"myexampleapp/pkg/zgen/taskgen"
	"time"

	anclax_app "github.com/cloudcarver/anclax/pkg/app"
	anclax_config "github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	anclax_wire "github.com/cloudcarver/anclax/wire"
	"github.com/gofiber/fiber/v2"
)

// This will run before the application starts.
func Init(anclaxApp *anclax_app.Application, taskrunner taskgen.TaskRunner, myapp anclax_app.Plugin, model model.ModelInterface) (*App, error) {
	if err := anclaxApp.Plug(myapp); err != nil {
		return nil, err
	}

	// Add your custom initialization logic here.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := anclaxApp.GetService().CreateNewUser(ctx, "test", "test"); err != nil {
		return nil, err
	}

	return &App{
		AnclaxApp: anclaxApp,
	}, nil
}

// InitAnclaxApplication initializes the Anclax application with the provided configuration.
// You can modify this function to customize the initialization process,
func InitAnclaxApplication(cfg *config.Config) (*anclax_app.Application, error) {
	anclaxApp, err := anclax_wire.InitializeApplication(&cfg.Anclax, anclax_config.DefaultLibConfig())
	if err != nil {
		return nil, err
	}

	return anclaxApp, nil
}

type App struct {
	AnclaxApp *anclax_app.Application
}

func (a *App) Start() error {
	return a.AnclaxApp.Start()
}

func (a *App) Close() {
	a.AnclaxApp.Close()
}

type Plugin struct {
	serverInterface apigen.ServerInterface
	validator       apigen.Validator
	taskHandler     worker.TaskHandler
}

func NewPlugin(serverInterface apigen.ServerInterface, validator apigen.Validator, taskHandler worker.TaskHandler) anclax_app.Plugin {
	return &Plugin{
		serverInterface: serverInterface,
		validator:       validator,
		taskHandler:     taskHandler,
	}
}

func (p *Plugin) PlugTo(anclaxApp *anclax_app.Application) error {
	p.plugToFiberApp(anclaxApp.GetServer().GetApp())
	p.plugToWorker(anclaxApp.GetWorker())
	return nil
}

func (p *Plugin) plugToFiberApp(fiberApp *fiber.App) {
	apigen.RegisterHandlersWithOptions(fiberApp, apigen.NewXMiddleware(p.serverInterface, p.validator), apigen.FiberServerOptions{
		BaseURL:     "/api/v1",
		Middlewares: []apigen.MiddlewareFunc{},
	})
}

func (p *Plugin) plugToWorker(worker worker.WorkerInterface) {
	worker.RegisterTaskHandler(p.taskHandler)
}
