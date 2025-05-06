package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

const (
	OapiCodegen = "oapi-codegen"
	Wire        = "wire"
	Sqlc        = "sqlc"
	Mockgen     = "mockgen"

	binDir = "bin"
)

var installMap = map[string]string{
	OapiCodegen: "github.com/deepmap/oapi-codegen/v2/cmd/oapi-codegen",
	Wire:        "github.com/google/wire/cmd/wire",
	Sqlc:        "github.com/sqlc-dev/sqlc/cmd/sqlc",
	Mockgen:     "go.uber.org/mock/mockgen",
}

var initCmd = &cli.Command{
	Name:  "init",
	Usage: "Initialize a new project in the current directory",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Usage: "Path to the config file",
			Value: "anchor.yaml",
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

func runGenInit(c *cli.Context) error {
	configPath := c.String("config")
	if configPath == "" {
		return errors.New("config path is required")
	}

	projectDir := c.Args().First()
	if projectDir == "" {
		return errors.New("missing project directory, use `anchor init <project-dir>`")
	}

	config, err := parseConfig(configPath)
	if err != nil {
		return errors.Wrap(err, "failed to parse config")
	}

	store, err := NewStore(projectDir)
	if err != nil {
		return errors.Wrap(err, "failed to create store")
	}

	installDir := filepath.Join(store.Path(), binDir)

	for external, targetVersion := range config.Externals {
		url := installMap[external]
		lastVersion := store.metadata.ExternalVersion[external]
		if url == "" {
			return errors.New("unknown external tool: " + external)
		}
		_, err := os.Stat(filepath.Join(installDir, external))
		if err != nil && !os.IsNotExist(err) {
			return errors.Wrap(err, "failed to check if external tool is installed")
		}

		if err == nil && targetVersion == lastVersion {
			continue
		}

		if os.IsNotExist(err) || targetVersion != lastVersion {
			if err := installExternal(installDir, url, config.Externals[external]); err != nil {
				return errors.Wrap(err, "failed to install external tool")
			}
			store.metadata.ExternalVersion[external] = config.Externals[external]
			if err := store.Save(); err != nil {
				return errors.Wrap(err, "failed to persist external tool version")
			}
		}

	}
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
