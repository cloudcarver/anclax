package injection

import (
	anchor_app "github.com/cloudcarver/anchor/pkg/app"
	"github.com/cloudcarver/anchor/pkg/auth"
	"github.com/cloudcarver/anchor/pkg/taskcore"
)

func InjectAuth(anchorApp *anchor_app.Application) auth.AuthInterface {
	return anchorApp.GetAuth()
}

func InjectTaskStore(anchorApp *anchor_app.Application) taskcore.TaskStoreInterface {
	return anchorApp.GetTaskStore()
}
