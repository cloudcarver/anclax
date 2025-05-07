package main

import (
	"flag"
	"fmt"

	"github.com/cloudcarver/anchor/pkg/logger"
	"github.com/cloudcarver/anchor/pkg/utils"
	"github.com/cloudcarver/anchor/wire"
	"go.uber.org/zap"
)

var log = logger.NewLogAgent("main")

var (
	version bool
)

func main() {
	flag.BoolVar(&version, "version", false, "version")
	flag.Parse()

	if version {
		fmt.Println(utils.CurrentVersion)
		return
	}

	app, err := wire.InitializeApplication()
	if err != nil {
		log.Error("failed to initialize server", zap.Error(err))
		panic(err)
	}

	if err := app.Start(); err != nil {
		log.Error("exit with error", zap.Error(err))
	}

	log.Info("bye.")
}
