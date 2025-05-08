package main

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

var installCmd = &cli.Command{
	Name:   "install",
	Usage:  "Install external tools",
	Action: runInstall,
}

func runInstall(c *cli.Context) error {
	projectDir := c.Args().Get(0)
	if projectDir == "" {
		return errors.New("missing project directory, use `anchor init <project-dir> <go-module-name>`")
	}
	return install(projectDir)
}

func install(projectDir string) error {
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
