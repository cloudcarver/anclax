package main

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

const (
	Anchor      = "anclax"
	OapiCodegen = "oapi-codegen"
	Wire        = "wire"
	Sqlc        = "sqlc"
	Mockgen     = "mockgen"

	binDir = "bin"
	tmpDir = "tmp"
)

var initCmd = &cli.Command{
	Name:  "init",
	Usage: "Initialize a new project in the current directory",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Usage: "Path to the config file",
			Value: "anclax.yaml",
		},
	},
	Action: runGenInit,
}

func parseConfig(configPath string) (*Config, error) {
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config Config
	if err = yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

var goModules = []string{
	"github.com/jackc/pgx/v5",
	"github.com/gofiber/fiber/v2",
	"github.com/google/wire",
	"github.com/cloudcarver/anclax",
}

func runGenInit(c *cli.Context) error {
	projectDir := c.Args().Get(0)
	if projectDir == "" {
		return errors.New("missing project directory, use `anclax init <project-dir> <go-module-name>`")
	}

	goModule := c.Args().Get(1)
	if goModule == "" {
		return errors.New("missing go module name, use `anclax init <project-dir> <go-module-name>`")
	}

	configName := c.String("config")
	if configName == "" {
		return errors.New("config name cannot be empty")
	}

	// create project directory
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return errors.Wrap(err, "failed to create project directory")
	}

	// Copy template files to the project directory
	if err := initFiles(projectDir, goModule); err != nil {
		return errors.Wrap(err, "failed to initialize project files")
	}

	// init go modules
	for _, module := range goModules {
		cmd := exec.Command("go", "get", "-u", module)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = projectDir
		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "failed to get go module")
		}
	}

	// go mod tidy
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = projectDir
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "failed to run go mod tidy")
	}

	// install external tools
	if err := install(projectDir, configName); err != nil {
		return errors.Wrap(err, "failed to install external tools")
	}

	// run codegen
	if err := codegen(configName, projectDir); err != nil {
		return errors.Wrap(err, "failed to run codegen")
	}

	fmt.Printf("Project initialized successfully in %s\n", projectDir)
	return nil
}

func installExternal(dir, url, version string) error {
	fmt.Println("Installing", url, "version", version, "to", dir)
	cmd := exec.Command("go", "install", fmt.Sprintf("%s@%s", url, version))
	cmd.Env = append(os.Environ(), "GOBIN="+dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

//go:embed all:initFiles
var files embed.FS

func initFiles(dir, goModule string) error {
	// create go.mod file

	// replicate all necessary files
	return fs.WalkDir(files, "initFiles", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get the relative path from the embedded root
		relPath, err := filepath.Rel("initFiles", path)
		if err != nil {
			return errors.Wrap(err, "failed to get relative path")
		}

		// Skip the root directory
		if relPath == "." {
			return nil
		}

		// Destination path
		dstPath := filepath.Join(dir, relPath)

		if d.IsDir() {
			// Create directory
			if err := os.MkdirAll(dstPath, 0755); err != nil {
				return errors.Wrap(err, "failed to create directory")
			}
		} else {
			// Read and write file
			content, err := files.ReadFile(path)
			if err != nil {
				return errors.Wrap(err, "failed to read file")
			}

			dstPath = strings.TrimSuffix(dstPath, ".embed")
			content = bytes.ReplaceAll(content, []byte("myexampleapp"), []byte(goModule))

			if err := os.WriteFile(dstPath, content, 0644); err != nil {
				return errors.Wrap(err, "failed to write file")
			}
		}

		return nil
	})
}
