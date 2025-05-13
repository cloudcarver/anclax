package main

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

var installMap = map[string]string{
	OapiCodegen: "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen",
	Wire:        "github.com/google/wire/cmd/wire",
	Sqlc:        "github.com/sqlc-dev/sqlc/cmd/sqlc",
	Mockgen:     "go.uber.org/mock/mockgen",
	Anchor:      "github.com/cloudcarver/anchor/cmd/anchor",
}

var installCmd = &cli.Command{
	Name:  "install",
	Usage: "Install external tools",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Usage: "Path to the config file",
			Value: "anchor.yaml",
		},
	},
	Action: runInstall,
}

func runInstall(c *cli.Context) error {
	projectDir := c.Args().Get(0)
	if projectDir == "" {
		return errors.New("missing project directory, use `anchor install <project-dir>`")
	}

	configName := c.String("config")
	if configName == "" {
		return errors.New("config name cannot be empty")
	}

	return install(projectDir, configName)
}

func install(projectDir, configName string) error {
	// install external tools
	config, err := parseConfig(configName)
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
