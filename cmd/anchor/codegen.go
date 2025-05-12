package main

import (
	"os"
	"os/exec"
	"path/filepath"

	task_codegen "github.com/cloudcarver/anchor/pkg/codegen/task"
	xware_codegen "github.com/cloudcarver/anchor/pkg/codegen/xware"
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

func genTaskHandler(workdir string, config *TaskHandlerConfig) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(workdir, config.Out)), 0755); err != nil {
		return errors.Wrap(err, "failed to create output directory")
	}
	return task_codegen.Generate(workdir, config.Package, config.Path, config.Out)
}

func genXware(workdir string, config *XwareConfig) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(workdir, config.Out)), 0755); err != nil {
		return errors.Wrap(err, "failed to create output directory")
	}
	return xware_codegen.Generate(workdir, config.Package, config.Path, config.Out)
}

func runGen(c *cli.Context) error {
	configPath := c.String("config")
	if configPath == "" {
		return errors.New("config is required")
	}

	workdir := c.Args().First()
	if workdir == "" {
		return errors.New("work directory is required, e.g. anchor gen .")
	}
	return codegen(c.String("config"), c.Args().First())
}

func codegen(configPath string, workdir string) error {
	configPath = filepath.Join(workdir, configPath)

	config, err := parseConfig(configPath)
	if err != nil {
		return errors.Wrap(err, "failed to parse config")
	}

	if config.Xware != nil {
		if err := genXware(workdir, config.Xware); err != nil {
			return errors.Wrap(err, "failed to generate xware")
		}
	}

	if config.TaskHandler != nil {
		if err := genTaskHandler(workdir, config.TaskHandler); err != nil {
			return errors.Wrap(err, "failed to generate task handler")
		}
	}

	if config.OapiCodegen != nil {
		if err := genOapi(workdir, config.OapiCodegen); err != nil {
			return errors.Wrap(err, "failed to generate oapi-codegen")
		}
	}

	if config.Sqlc != nil {
		if err := genSqlc(workdir, config.Sqlc); err != nil {
			return errors.Wrap(err, "failed to generate sqlc")
		}
	}

	if config.Mockgen != nil {
		if err := genMock(workdir, config.Mockgen); err != nil {
			return errors.Wrap(err, "failed to generate mockgen")
		}
	}

	if config.Wire != nil {
		if err := genWire(workdir, config.Wire); err != nil {
			return errors.Wrap(err, "failed to generate wire")
		}
	}

	return nil
}

func command(name string) string {
	return filepath.Join(storePath, binDir, name)
}

func genOapi(workdir string, config *OapiCodegenConfig) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(workdir, config.Out)), 0755); err != nil {
		return errors.Wrap(err, "failed to create output directory")
	}
	cmd := exec.Command(command("oapi-codegen"), "-generate", "types,fiber,client", "-package", config.Package, "-o", config.Out, config.Path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = workdir
	return cmd.Run()
}

func genWire(workdir string, config *WireConfig) error {
	cmd := exec.Command(command("wire"), config.Path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = workdir
	return cmd.Run()
}

func genSqlc(workdir string, config *SqlcConfig) error {
	cmd := exec.Command(command("sqlc"), "generate", "--file", config.Path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = workdir
	return cmd.Run()
}

func genMock(workdir string, config *MockgenConfig) error {
	for _, file := range config.Files {
		cmd := exec.Command(command("mockgen"), "-source", file.Source, "-destination", file.Destination, "-package", file.Package)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = workdir
		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "failed to generate mockgen")
		}
	}
	return nil
}
