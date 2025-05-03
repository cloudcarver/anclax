package main

import (
	"errors"
	"log"
	"os"

	"github.com/cloudcarver/anchor/internal/codegen"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

func main() {
	app := &cli.App{
		Name:  "anchor",
		Usage: "Anchor is a tool for quickly building production-ready web applications with confidence",
		Commands: []*cli.Command{
			{
				Name:  "gen",
				Usage: "Generate code",
				Subcommands: []*cli.Command{
					{
						Name:   "task",
						Usage:  "Generate task related code",
						Action: runGenTask,
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:  "task-def-path",
								Usage: "Path to the task definition file",
								Value: "api/tasks.yaml",
							},
							&cli.StringFlag{
								Name:     "out-path",
								Usage:    "Path to the output file",
								Required: true,
							},
							&cli.StringFlag{
								Name:     "package-name",
								Usage:    "Package name",
								Required: true,
							},
						},
					},
				},
				Action: runGen,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func runGen(c *cli.Context) error {
	if err := runGenTask(c); err != nil {
		return err
	}
	return nil
}

func runGenTask(c *cli.Context) error {
	taskDefPath := c.String("task-def-path")
	if taskDefPath == "" {
		return errors.New("task-def-path is required")
	}

	outPath := c.String("out-path")
	packageName := c.String("package-name")

	raw, err := os.ReadFile(taskDefPath)
	if err != nil {
		return err
	}
	var data map[string]any
	if err := yaml.Unmarshal(raw, &data); err != nil {
		return err
	}

	codegen.ResetGlobalTypeNameCounter()

	result, err := codegen.GenerateToolInterfaces(packageName, data)
	if err != nil {
		return err
	}

	if err := os.WriteFile(outPath, []byte(result), 0644); err != nil {
		return err
	}

	return nil
}
