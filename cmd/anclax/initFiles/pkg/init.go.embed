package pkg

import (
	"context"
	"myexampleapp/pkg/config"
	"myexampleapp/pkg/zcore/app"
	"myexampleapp/pkg/zcore/model"
	"myexampleapp/pkg/zgen/taskgen"
	"time"

	anclax_config "github.com/cloudcarver/anclax/pkg/config"
	anclax_wire "github.com/cloudcarver/anclax/wire"

	anclax_app "github.com/cloudcarver/anclax/pkg/app"
)

func ProvidePluginMeta() anclax_app.PluginMeta {
	return anclax_app.PluginMeta{
		// This field is for avoiding conflicts with other Anclax plugins.
		// It will be used as the table name of the migration table.
		// You can change it to any string that is unique in your application.
		//
		// [IMPORTANT]
		// This field should NOT be changed after the application is deployed.
		Namespace: "myapp",
	}
}

// This will run before the application starts.
func Init(anclaxApp *anclax_app.Application, taskrunner taskgen.TaskRunner, myapp anclax_app.Plugin, model model.ModelInterface) (*app.App, error) {
	if err := anclaxApp.Plug(myapp); err != nil {
		return nil, err
	}

	// Add your custom initialization logic here.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := anclaxApp.GetService().CreateNewUser(ctx, "test", "test"); err != nil {
		return nil, err
	}
	// if _, err := taskrunner.RunAutoIncrementCounter(ctx, &taskgen.AutoIncrementCounterParameters{
	// 	Amount: 1,
	// }, taskcore.WithUniqueTag("auto-increment-counter")); err != nil {
	// 	return nil, err
	// }

	// closer
	anclaxApp.RegisterCloser(model.Close)

	return &app.App{
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
