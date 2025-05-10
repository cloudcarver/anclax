package main

import (
	"fmt"
	"os"

	"github.com/cloudcarver/anchor/dev/tools"
	"github.com/urfave/cli/v2"
)

var rootCmd = &cli.App{
	Name:  "dev",
	Usage: "dev tools",
	Commands: []*cli.Command{
		{
			Name: "copy-templates",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:     "src",
					Required: true,
					Usage:    "source directory",
				},
				&cli.StringFlag{
					Name:     "dst",
					Usage:    "destination directory",
					Required: true,
				},
				&cli.StringSliceFlag{
					Name:  "exclude",
					Usage: "exclude files",
					Value: cli.NewStringSlice(),
				},
			},
			Action: copyTemplates,
		},
	},
}

func copyTemplates(c *cli.Context) error {
	src := c.String("src")
	dst := c.String("dst")
	exclude := c.StringSlice("exclude")
	if src == "" || dst == "" {
		return fmt.Errorf("source and destination are required")
	}

	return tools.CopyToInitFiles(src, dst, exclude)
}

func main() {
	if err := rootCmd.Run(os.Args); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
