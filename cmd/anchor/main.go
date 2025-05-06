package main

import (
	"log"
	"os"

	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "anchor",
		Usage: "Anchor is a tool for quickly building production-ready web applications with confidence",
		Commands: []*cli.Command{
			genCmd,
			initCmd,
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
