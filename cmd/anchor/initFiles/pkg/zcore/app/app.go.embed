package app

import (
	"myexampleapp/pkg/zgen/apigen"
	"regexp"
	"strings"

	anchor_app "github.com/cloudcarver/anchor/pkg/app"
	"github.com/cloudcarver/anchor/pkg/taskcore/worker"
	"github.com/gofiber/fiber/v2"
)

const projectPkg = "myexampleapp"

func GetNamespace() string {
	last := projectPkg
	if idx := strings.LastIndex(projectPkg, "/"); idx != -1 {
		last = projectPkg[idx+1:]
	}
	return regexp.MustCompile(`[^a-z0-9_]`).ReplaceAllString(strings.ToLower(last), "_")
}

type App struct {
	AnchorApp *anchor_app.Application
}

func (a *App) Start() error {
	return a.AnchorApp.Start()
}

type Plugin struct {
	serverInterface apigen.ServerInterface
	validator       apigen.Validator
	taskHandler     worker.TaskHandler
}

func NewPlugin(serverInterface apigen.ServerInterface, validator apigen.Validator, taskHandler worker.TaskHandler) anchor_app.Plugin {
	return &Plugin{
		serverInterface: serverInterface,
		validator:       validator,
		taskHandler:     taskHandler,
	}
}

func (p *Plugin) PlugTo(anchorApp *anchor_app.Application) error {
	p.plugToFiberApp(anchorApp.GetServer().GetApp())
	p.plugToWorker(anchorApp.GetWorker())
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
