//go:build wireinject
// +build wireinject

package wire

import (
	"github.com/cloudcarver/anchor/internal/apps/server"
	"github.com/cloudcarver/anchor/internal/auth"
	"github.com/cloudcarver/anchor/internal/config"
	"github.com/cloudcarver/anchor/internal/controller"
	"github.com/cloudcarver/anchor/internal/model"
	"github.com/cloudcarver/anchor/internal/service"
	"github.com/google/wire"
)

func InitializeServer() (*server.Server, error) {
	wire.Build(
		config.NewConfig,
		service.NewService,
		controller.NewController,
		model.NewModel,
		server.NewServer,
		auth.NewAuthStore,
		auth.NewAuth,
	)
	return nil, nil
}
