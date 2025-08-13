package pkg

import (
	"context"
	"myexampleapp/pkg/config"
	"myexampleapp/pkg/zcore/app"
	"myexampleapp/pkg/zgen/taskgen"

	anchor_config "github.com/cloudcarver/anchor/pkg/config"
	anchor_wire "github.com/cloudcarver/anchor/wire"

	anchor_app "github.com/cloudcarver/anchor/pkg/app"
	"github.com/cloudcarver/anchor/pkg/taskcore"
)

// This will run before the application starts.
func Init(anchorApp *anchor_app.Application, taskrunner taskgen.TaskRunner, myapp anchor_app.Plugin) (*app.App, error) {
	if err := anchorApp.Plug(myapp); err != nil {
		return nil, err
	}

	// Add your custom initialization logic here.
	if _, err := anchorApp.GetService().CreateNewUser(context.Background(), "test", "test"); err != nil {
		return nil, err
	}
	if _, err := taskrunner.RunAutoIncrementCounter(context.Background(), &taskgen.AutoIncrementCounterParameters{
		Amount: 1,
	}, taskcore.WithUniqueTag("auto-increment-counter")); err != nil {
		return nil, err
	}

	return &app.App{
		AnchorApp: anchorApp,
	}, nil
}

// InitAnchorApplication initializes the Anchor application with the provided configuration.
// You can modify this function to customize the initialization process,
func InitAnchorApplication(cfg *config.Config) (*anchor_app.Application, error) {
	anchorApp, err := anchor_wire.InitializeApplication(&cfg.Anchor, anchor_config.DefaultLibConfig())
	if err != nil {
		return nil, err
	}

	return anchorApp, nil
}
