package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudcarver/anclax/lib/dst"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "dst",
		Usage: "Distributed System Test tooling",
		Commands: []*cli.Command{
			validateCmd,
			genCmd,
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var validateCmd = &cli.Command{
	Name:  "validate",
	Usage: "Validate a DST YAML spec",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Required: true, Usage: "path to dst spec yaml"},
	},
	Action: func(c *cli.Context) error {
		spec, err := dst.LoadHybridSpecFromFile(c.String("file"))
		if err != nil {
			return err
		}
		if err := dst.ValidateHybridSpec(spec); err != nil {
			return err
		}
		fmt.Printf("valid: %d scenario(s)\n", len(spec.Scenarios))
		return nil
	},
}

var genCmd = &cli.Command{
	Name:  "gen",
	Usage: "Generate Go interfaces + scenario runner from DST YAML spec",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Required: true, Usage: "path to dst spec yaml"},
		&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: "dstgen/generated.go", Usage: "output go file path"},
		&cli.StringFlag{Name: "pkg", Usage: "override output package name (default from yaml or dstgen)"},
	},
	Action: func(c *cli.Context) error {
		spec, err := dst.LoadHybridSpecFromFile(c.String("file"))
		if err != nil {
			return err
		}
		if err := dst.ValidateHybridSpec(spec); err != nil {
			return err
		}
		code, err := dst.GenerateHybridGo(spec, c.String("pkg"))
		if err != nil {
			return err
		}

		outPath := c.String("out")
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return errors.Wrap(err, "create output directory")
		}
		if err := os.WriteFile(outPath, []byte(code), 0o644); err != nil {
			return errors.Wrap(err, "write generated code")
		}
		fmt.Printf("generated: %s\n", outPath)
		return nil
	},
}
