package main

import (
	"embed"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudcarver/anclax"
	dst_codegen "github.com/cloudcarver/anclax/lib/dst"
	"github.com/cloudcarver/anclax/pkg/codegen/codegenpath"
	oapi_codegen "github.com/cloudcarver/anclax/pkg/codegen/oapi"
	schema_codegen "github.com/cloudcarver/anclax/pkg/codegen/schemas"
	task_codegen "github.com/cloudcarver/anclax/pkg/codegen/task"
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
			Value: "anclax.yaml",
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
			Value: "anclax.yaml",
		},
	},
	Action: runClean,
}

func copyEmbedDir(fs embed.FS, srcDir, workdir, destDir string) error {
	entries, err := fs.ReadDir(srcDir)
	if err != nil {
		return errors.Wrapf(err, "failed to read embedded directory %s", srcDir)
	}

	if err := codegenpath.MkdirAll(workdir, destDir, 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory %s", destDir)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := copyEmbedDir(fs, filepath.Join(srcDir, entry.Name()), workdir, filepath.Join(destDir, entry.Name())); err != nil {
				return err
			}
		} else {
			content, err := fs.ReadFile(filepath.Join(srcDir, entry.Name()))
			if err != nil {
				return errors.Wrapf(err, "failed to read embedded file %s", filepath.Join(srcDir, entry.Name()))
			}
			if err := codegenpath.WriteFile(workdir, filepath.Join(destDir, entry.Name()), content, 0644); err != nil {
				return errors.Wrapf(err, "failed to write embedded file %s", filepath.Join(destDir, entry.Name()))
			}
		}
	}
	return nil
}

func writeAnclaxDef(workdir, outdir string) error {
	if _, err := codegenpath.Resolve(workdir, outdir); err != nil {
		return errors.Wrap(err, "invalid anclax def directory")
	}
	if err := codegenpath.MkdirAll(workdir, outdir, 0755); err != nil {
		return errors.Wrap(err, "failed to create anclax def directory")
	}

	// write migrations files
	if err := copyEmbedDir(anclax.Migrations, "sql", workdir, filepath.Join(outdir, "sql")); err != nil {
		return errors.Wrap(err, "failed to copy migrations files")
	}

	// write api spec files
	if err := copyEmbedDir(anclax.API, "api", workdir, filepath.Join(outdir, "api")); err != nil {
		return errors.Wrap(err, "failed to copy api spec files")
	}

	return nil
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
		workdir = "."
	}

	stagingDir, err := codegenpath.MkdirTemp(workdir, ".anclax-codegen-")
	if err != nil {
		return errors.Wrap(err, "failed to create temporary directory")
	}
	defer codegenpath.RemoveAll(workdir, stagingDir)

	return clean(stagingDir, config, workdir)
}

func genTaskHandler(workdir string, config *TaskHandlerConfig, schemasConfig *SchemasConfig) error {
	var schemaCfg *schema_codegen.Config
	if schemasConfig != nil {
		schemaCfg = &schema_codegen.Config{Path: schemasConfig.Path, Output: schemasConfig.Output}
	}
	return task_codegen.Generate(workdir, config.Package, config.Path, config.Out, schemaCfg)
}

func genSchemas(workdir string, config *SchemasConfig) error {
	if config == nil {
		return nil
	}
	manager, err := schema_codegen.Load(workdir, schema_codegen.Config{Path: config.Path, Output: config.Output})
	if err != nil {
		return err
	}
	if manager == nil {
		return nil
	}
	return manager.Generate()
}

func genDST(workdir string, config *DSTConfig) error {
	if config.Path == "" {
		return errors.New("dst path is required")
	}
	if config.Out == "" {
		return errors.New("dst out is required")
	}

	specPath, err := codegenpath.ResolveRead(workdir, config.Path)
	if err != nil {
		return errors.Wrap(err, "invalid dst input path")
	}
	if _, err := codegenpath.Resolve(workdir, config.Out); err != nil {
		return errors.Wrap(err, "invalid dst out")
	}
	spec, err := dst_codegen.LoadHybridSpecFromFile(specPath)
	if err != nil {
		return errors.Wrap(err, "failed to load dst spec")
	}
	if err := dst_codegen.ValidateHybridSpec(spec); err != nil {
		return errors.Wrap(err, "failed to validate dst spec")
	}
	code, err := dst_codegen.GenerateHybridGo(spec, config.Package)
	if err != nil {
		return errors.Wrap(err, "failed to generate dst code")
	}

	if err := codegenpath.WriteFile(workdir, config.Out, []byte(code), 0644); err != nil {
		return errors.Wrap(err, "failed to write dst generated code")
	}
	return nil
}

func runGen(c *cli.Context) error {
	configPath := c.String("config")
	if configPath == "" {
		return errors.New("config is required")
	}

	workdir := c.Args().First()
	if workdir == "" {
		workdir = "."
	}
	return codegen(c.String("config"), workdir)
}

func clean(stagingDir string, config *Config, workdir string) error {
	type pendingMove struct {
		source   string
		rel      string
		target   string
		identity string
	}
	pendingByIdentity := map[string]pendingMove{}
	for _, pattern := range config.CleanItems {
		resolvedPattern, err := codegenpath.ResolvePattern(workdir, pattern)
		if err != nil {
			return errors.Wrapf(err, "invalid clean pattern %s", pattern)
		}
		matches, err := filepath.Glob(resolvedPattern)
		if err != nil {
			return errors.Wrapf(err, "failed to glob pattern %s", pattern)
		}

		for _, match := range matches {
			relPath, err := codegenpath.RelEntry(workdir, match)
			if err != nil {
				return errors.Wrapf(err, "clean match %s escapes workdir", match)
			}
			resolvedParent, err := codegenpath.Resolve(workdir, filepath.Dir(relPath))
			if err != nil {
				return errors.Wrapf(err, "invalid clean parent for %s", relPath)
			}
			identity := filepath.Join(resolvedParent, filepath.Base(relPath))
			canonicalRel, err := codegenpath.RelEntry(workdir, identity)
			if err != nil {
				return errors.Wrapf(err, "invalid clean match %s", relPath)
			}
			if pathContains(stagingDir, canonicalRel) {
				continue
			}
			pendingByIdentity[identity] = pendingMove{
				source:   match,
				rel:      canonicalRel,
				target:   filepath.Join(stagingDir, canonicalRel),
				identity: identity,
			}
		}
	}

	pending := make([]pendingMove, 0, len(pendingByIdentity))
	for _, move := range pendingByIdentity {
		pending = append(pending, move)
	}
	sort.Slice(pending, func(i, j int) bool {
		if len(pending[i].identity) != len(pending[j].identity) {
			return len(pending[i].identity) < len(pending[j].identity)
		}
		return pending[i].identity < pending[j].identity
	})
	selected := pending[:0]
	for _, move := range pending {
		covered := false
		for _, parent := range selected {
			if pathContains(parent.identity, move.identity) {
				covered = true
				break
			}
		}
		if !covered {
			selected = append(selected, move)
		}
	}

	var moved []pendingMove
	rollback := func(cause error) error {
		for i := len(moved) - 1; i >= 0; i-- {
			move := moved[i]
			err := codegenpath.Rename(workdir, move.target, move.rel)
			if err != nil {
				return errors.Wrapf(cause, "also failed to roll back clean path %s: %v", move.rel, err)
			}
		}
		return cause
	}

	for _, move := range selected {
		if err := codegenpath.Rename(workdir, move.rel, move.target); err != nil {
			return rollback(errors.Wrapf(err, "failed to move %s to temp directory", move.source))
		}
		moved = append(moved, move)
	}
	return nil
}

func restore(stagingDir string, _ *Config, workdir string) error {
	stagingPath, err := codegenpath.Resolve(workdir, stagingDir)
	if err != nil {
		return errors.Wrap(err, "invalid restore staging directory")
	}
	return filepath.Walk(stagingPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Get relative path from tempDir to properly restore
		relPath, err := filepath.Rel(stagingPath, path)
		if err != nil {
			return errors.Wrapf(err, "failed to get relative path for %s", path)
		}

		if err := codegenpath.Rename(workdir, filepath.Join(stagingDir, relPath), relPath); err != nil {
			return errors.Wrapf(err, "failed to restore %s", relPath)
		}

		return nil
	})
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func codegen(configPath string, workdir string) error {
	if workdir == "" {
		workdir = "."
	}

	// parse config
	configPath = filepath.Join(workdir, configPath)
	config, err := parseConfig(configPath)
	if err != nil {
		return errors.Wrap(err, "failed to parse config")
	}
	if err := validateCodegenPaths(workdir, config); err != nil {
		return errors.Wrap(err, "failed to validate codegen paths")
	}

	stagingDir := ""
	if len(config.CleanItems) > 0 {
		stagingDir, err = codegenpath.MkdirTemp(workdir, ".anclax-codegen-")
		if err != nil {
			return errors.Wrap(err, "failed to create temporary directory")
		}
		defer codegenpath.RemoveAll(workdir, stagingDir)
	}

	// pre-codegen
	if stagingDir != "" {
		if err := clean(stagingDir, config, workdir); err != nil {
			return errors.Wrap(err, "failed to pre-codegen")
		}
	}

	// codegen
	codegenErr := _codegen(config, workdir)

	// post-codegen
	if stagingDir != "" && codegenErr != nil {
		if err := restore(stagingDir, config, workdir); err != nil {
			return errors.Wrap(err, "failed to post-codegen")
		}
	}

	return codegenErr
}

func _codegen(config *Config, workdir string) error {
	if config.Schemas != nil {
		if err := genSchemas(workdir, config.Schemas); err != nil {
			return errors.Wrap(err, "failed to generate schemas")
		}
	}

	for i := range config.OapiCodegen {
		if err := genOapi(workdir, &config.OapiCodegen[i], config.Schemas); err != nil {
			return errors.Wrapf(err, "failed to generate oapi-codegen[%d]", i)
		}
	}

	for i := range config.TaskHandler {
		if err := genTaskHandler(workdir, &config.TaskHandler[i], config.Schemas); err != nil {
			return errors.Wrapf(err, "failed to generate task-handler[%d]", i)
		}
	}

	for i := range config.DST {
		if err := genDST(workdir, &config.DST[i]); err != nil {
			return errors.Wrapf(err, "failed to generate dst[%d]", i)
		}
	}

	for i := range config.Sqlc {
		if err := genSqlc(workdir, &config.Sqlc[i]); err != nil {
			return errors.Wrapf(err, "failed to generate sqlc[%d]", i)
		}
	}

	if config.Mockgen != nil {
		if err := genMock(workdir, config.Mockgen); err != nil {
			return errors.Wrap(err, "failed to generate mockgen")
		}
	}

	for i := range config.Wire {
		if err := genWire(workdir, &config.Wire[i]); err != nil {
			return errors.Wrapf(err, "failed to generate wire[%d]", i)
		}
	}

	if config.AnclaxDef != "" {
		if err := writeAnclaxDef(workdir, config.AnclaxDef); err != nil {
			return errors.Wrap(err, "failed to write anclax def")
		}
	}

	return nil
}

func command(name string) string {
	return filepath.Join(storePath, binDir, name)
}

func genOapi(workdir string, config *OapiCodegenConfig, schemasConfig *SchemasConfig) error {
	var schemaCfg *schema_codegen.Config
	if schemasConfig != nil {
		schemaCfg = &schema_codegen.Config{Path: schemasConfig.Path, Output: schemasConfig.Output}
	}
	return oapi_codegen.Generate(workdir, oapi_codegen.Config{
		Path:    config.Path,
		Out:     config.Out,
		Package: config.Package,
		Schemas: schemaCfg,
	})
}

func genWire(workdir string, config *WireConfig) error {
	wireDir, err := resolveWireDir(workdir, config.Path)
	if err != nil {
		return errors.Wrap(err, "invalid wire path")
	}
	cmd := exec.Command(command("wire"), wireDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = workdir
	return cmd.Run()
}

func genSqlc(workdir string, config *SqlcConfig) error {
	configPath, err := codegenpath.Resolve(workdir, config.Path)
	if err != nil {
		return errors.Wrap(err, "invalid sqlc path")
	}
	if err := validateSQLCOutputs(workdir, config.Path); err != nil {
		return errors.Wrap(err, "invalid sqlc outputs")
	}
	cmd := exec.Command(command("sqlc"), "generate", "--file", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = workdir
	return cmd.Run()
}

func genMock(workdir string, config *MockgenConfig) error {
	for _, file := range config.Files {
		source, err := codegenpath.ResolveRead(workdir, file.Source)
		if err != nil {
			return errors.Wrap(err, "invalid mockgen source")
		}
		destination, err := codegenpath.Resolve(workdir, file.Destination)
		if err != nil {
			return errors.Wrap(err, "invalid mockgen destination")
		}
		cmd := exec.Command(command("mockgen"), "-source", source, "-destination", destination, "-package", file.Package)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = workdir
		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "failed to generate mockgen")
		}
	}
	return nil
}
