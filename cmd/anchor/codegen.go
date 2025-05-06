package main

import (
	"os"
	"os/exec"
	"path/filepath"

	task_codegen "github.com/cloudcarver/anchor/internal/codegen/task"
	xware_codegen "github.com/cloudcarver/anchor/internal/codegen/xware"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

var genCmd = &cli.Command{
	Name:  "gen",
	Usage: "Generate code",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Usage: "Path to the config file",
			Value: "anchor.yaml",
		},
	},
	Action: runGen,
}

func genTaskHandler(config *TaskHandlerConfig) error {
	return task_codegen.Generate(config.Package, config.Path, config.Out)
}

func genXware(config *XwareConfig) error {
	return xware_codegen.Generate(config.Package, config.Path, config.Out)
}

func runGen(c *cli.Context) error {
	configPath := c.String("config")
	if configPath == "" {
		return errors.New("config is required")
	}

	config, err := parseConfig(configPath)
	if err != nil {
		return errors.Wrap(err, "failed to parse config")
	}

	if config.Xware != nil {
		if err := genXware(config.Xware); err != nil {
			return errors.Wrap(err, "failed to generate xware")
		}
	}

	if config.TaskHandler != nil {
		if err := genTaskHandler(config.TaskHandler); err != nil {
			return errors.Wrap(err, "failed to generate task handler")
		}
	}

	if config.OapiCodegen != nil {
		if err := genOapi(config.OapiCodegen); err != nil {
			return errors.Wrap(err, "failed to generate oapi-codegen")
		}
	}

	if config.Sqlc != nil {
		if err := genSqlc(config.Sqlc); err != nil {
			return errors.Wrap(err, "failed to generate sqlc")
		}
	}

	if config.Mockgen != nil {
		if err := genMock(config.Mockgen); err != nil {
			return errors.Wrap(err, "failed to generate mockgen")
		}
	}

	if config.Wire != nil {
		if err := genWire(config.Wire); err != nil {
			return errors.Wrap(err, "failed to generate wire")
		}
	}

	return nil
}

func command(name string) string {
	return filepath.Join(storePath, binDir, name)
}

func genOapi(config *OapiCodegenConfig) error {
	cmd := exec.Command(command("oapi-codegen"), "-generate", "types,fiber,client", "-package", config.Package, "-o", config.Out, config.Path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func genWire(config *WireConfig) error {
	cmd := exec.Command(command("wire"), config.Path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func genSqlc(config *SqlcConfig) error {
	cmd := exec.Command(command("sqlc"), "generate")
	if config.Path != "" {
		cmd.Args = append(cmd.Args, config.Path)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func genMock(config *MockgenConfig) error {
	for _, file := range config.Files {
		cmd := exec.Command(command("mockgen"), "-source", file.Source, "-destination", file.Destination, "-package", file.Package)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	return nil
}
