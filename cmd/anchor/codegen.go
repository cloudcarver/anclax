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

var cleanCmd = &cli.Command{
	Name:  "clean",
	Usage: "Clean files specified in the config",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Usage: "Path to the config file",
			Value: "anchor.yaml",
		},
	},
	Action: runClean,
}

func runClean(c *cli.Context) error {
	configPath := c.String("config")
	if configPath == "" {
		return errors.New("config is required")
	}

	config, err := parseConfig(configPath)
	if err != nil {
		return errors.Wrap(err, "failed to parse config")
	}

	workdir := c.Args().First()
	if workdir == "" {
		return errors.New("work directory is required, e.g. anchor clean .")
	}

	tempDir, err := os.MkdirTemp("", "anchor-codegen-")
	if err != nil {
		return errors.Wrap(err, "failed to create temporary directory")
	}
	defer os.RemoveAll(tempDir)

	return clean(tempDir, config, workdir)
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

func clean(tempDir string, config *Config, workdir string) error {
	for _, pattern := range config.CleanItems {
		matches, err := filepath.Glob(filepath.Join(workdir, pattern))
		if err != nil {
			return errors.Wrapf(err, "failed to glob pattern %s", pattern)
		}

		for _, match := range matches {
			// Create target directory in temp folder with the same relative structure
			relPath, err := filepath.Rel(workdir, match)
			if err != nil {
				return errors.Wrapf(err, "failed to get relative path for %s", match)
			}

			targetDir := filepath.Dir(filepath.Join(tempDir, relPath))
			if err := os.MkdirAll(targetDir, 0755); err != nil {
				return errors.Wrapf(err, "failed to create temp directory for %s", relPath)
			}

			// Move the file to temp directory
			targetPath := filepath.Join(tempDir, relPath)
			if err := os.Rename(match, targetPath); err != nil {
				return errors.Wrapf(err, "failed to move %s to temp directory", match)
			}
		}
	}
	return nil
}

func restore(tempDir string, config *Config, workdir string) error {
	return filepath.Walk(tempDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Get relative path from tempDir to properly restore
		relPath, err := filepath.Rel(tempDir, path)
		if err != nil {
			return errors.Wrapf(err, "failed to get relative path for %s", path)
		}

		// Create target directory if it doesn't exist
		destPath := filepath.Join(workdir, relPath)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return errors.Wrapf(err, "failed to create directory for restoring %s", relPath)
		}

		// Move file back
		if err := os.Rename(path, destPath); err != nil {
			return errors.Wrapf(err, "failed to restore %s", relPath)
		}

		return nil
	})
}

func codegen(configPath string, workdir string) error {
	tempDir, err := os.MkdirTemp("", "anchor-codegen-")
	if err != nil {
		return errors.Wrap(err, "failed to create temporary directory")
	}
	defer os.RemoveAll(tempDir)

	preCodegen := func(config *Config) error {
		if len(config.CleanItems) == 0 {
			return nil
		}
		if err := clean(tempDir, config, workdir); err != nil {
			return errors.Wrap(err, "failed to clean")
		}
		return nil
	}

	postCodegen := func(config *Config, codegenErr error) error {
		if len(config.CleanItems) == 0 {
			return nil
		}
		if codegenErr == nil {
			return nil
		}
		// If there was an error, restore the files from temp directory
		if err := restore(tempDir, config, workdir); err != nil {
			return err
		}
		return nil
	}

	// parse config
	configPath = filepath.Join(workdir, configPath)
	config, err := parseConfig(configPath)
	if err != nil {
		return errors.Wrap(err, "failed to parse config")
	}

	// pre-codegen
	if err := preCodegen(config); err != nil {
		return errors.Wrap(err, "failed to pre-codegen")
	}

	// codegen
	codegenErr := _codegen(config, workdir)

	// post-codegen
	if err := postCodegen(config, codegenErr); err != nil {
		return errors.Wrap(err, "failed to post-codegen")
	}

	return codegenErr
}

func _codegen(config *Config, workdir string) error {
	if config.OapiCodegen != nil {
		if err := genOapi(workdir, config.OapiCodegen); err != nil {
			return errors.Wrap(err, "failed to generate oapi-codegen")
		}
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
