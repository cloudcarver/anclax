package main

import (
	"fmt"
	"log"
	"os"

	"github.com/urfave/cli/v2"
)

var versionCmd = &cli.Command{
	Name:  "version",
	Usage: "Show the version of anclax",
	Action: func(c *cli.Context) error {
		fmt.Println("v0.6.15")
		return nil
	},
}

func main() {
	app := &cli.App{
		Name:  "anclax",
		Usage: "Anclax is a tool for quickly building production-ready web applications with confidence",
		Commands: []*cli.Command{
			genCmd,
			initCmd,
			docsCmd,
			installCmd,
			versionCmd,
			cleanCmd,
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
