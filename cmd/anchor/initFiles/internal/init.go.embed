package internal

import (
	"context"
	"myexampleapp/internal/zgen/taskgen"

	anchor_app "github.com/cloudcarver/anchor/pkg/app"
	anchor_svc "github.com/cloudcarver/anchor/pkg/service"
	"github.com/cloudcarver/anchor/pkg/taskcore"
)

func Init(anchorSvc anchor_svc.ServiceInterface, taskrunner taskgen.TaskRunner) func(*anchor_app.Application) error {
	return func(_ *anchor_app.Application) error {
		if _, err := anchorSvc.CreateNewUser(context.Background(), "test", "test"); err != nil {
			return err
		}
		if _, err := taskrunner.RunAutoIncrementCounter(context.Background(), &taskgen.AutoIncrementCounterParameters{
			Amount: 1,
		}, taskcore.WithUniqueTag("auto-increment-counter")); err != nil {
			return err
		}
		return nil
	}
}
