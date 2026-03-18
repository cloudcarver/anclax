package app

import (
	anclax_app "github.com/cloudcarver/anclax/pkg/app"
	"github.com/cloudcarver/anclax/pkg/auth"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
)

func InjectAuth(anclaxApp *anclax_app.Application) auth.AuthInterface {
	return anclaxApp.GetAuth()
}

func InjectTaskStore(anclaxApp *anclax_app.Application) taskcore.TaskStoreInterface {
	return anclaxApp.GetTaskStore()
}
