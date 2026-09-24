package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudcarver/anclax/pkg/codegen/codegenpath"
)

func TestValidateCodegenPathsRejectsEveryConfiguredEscape(t *testing.T) {
	t.Parallel()

	fields := []struct {
		name string
		set  func(*Config, string)
	}{
		{name: "schemas.output", set: func(c *Config, path string) {
			c.Schemas = &SchemasConfig{Path: "api/schemas", Output: path}
		}},
		{name: "oapi-codegen.out", set: func(c *Config, path string) {
			c.OapiCodegen = []OapiCodegenConfig{{Path: "api/openapi", Out: path, Package: "apigen"}}
		}},
		{name: "task-handler.out", set: func(c *Config, path string) {
			c.TaskHandler = []TaskHandlerConfig{{Path: "api/tasks.yaml", Out: path, Package: "taskgen"}}
		}},
		{name: "dst.out", set: func(c *Config, path string) {
			c.DST = []DSTConfig{{Path: "api/dst.yaml", Out: path}}
		}},
		{name: "anclaxdef", set: func(c *Config, path string) {
			c.AnclaxDef = path
		}},
		{name: "mockgen.destination", set: func(c *Config, path string) {
			c.Mockgen = &MockgenConfig{Files: []MockgenFileConfig{{Source: "pkg/service.go", Destination: path, Package: "pkg"}}}
		}},
		{name: "wire.path", set: func(c *Config, path string) {
			c.Wire = []WireConfig{{Path: path}}
		}},
		{name: "sqlc.path", set: func(c *Config, path string) {
			c.Sqlc = []SqlcConfig{{Path: path}}
		}},
		{name: "clean", set: func(c *Config, path string) {
			c.CleanItems = []string{path}
		}},
	}
	escapes := []struct {
		name string
		path func(workdir, outside string) string
	}{
		{name: "absolute", path: func(_, outside string) string { return filepath.Join(outside, "generated.go") }},
		{name: "lexical", path: func(_, _ string) string { return filepath.Join("..", "outside", "generated.go") }},
		{name: "symlink", path: func(_, _ string) string { return filepath.Join("escape", "generated.go") }},
	}

	for _, field := range fields {
		for _, escape := range escapes {
			t.Run(field.name+"/"+escape.name, func(t *testing.T) {
				parent := t.TempDir()
				workdir := filepath.Join(parent, "repo")
				outside := filepath.Join(parent, "outside")
				if err := os.MkdirAll(workdir, 0755); err != nil {
					t.Fatalf("mkdir workdir: %v", err)
				}
				if err := os.MkdirAll(outside, 0755); err != nil {
					t.Fatalf("mkdir outside: %v", err)
				}
				if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
					t.Fatalf("create escaping symlink: %v", err)
				}
				sentinel := filepath.Join(outside, "sentinel")
				if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
					t.Fatalf("write sentinel: %v", err)
				}

				config := &Config{}
				field.set(config, escape.path(workdir, outside))
				if err := validateCodegenPaths(workdir, config); err == nil {
					t.Fatal("validateCodegenPaths() unexpectedly accepted an escaping path")
				}
				assertCodegenSentinel(t, sentinel)
			})
		}
	}
}

func TestValidateCodegenPathsAllowsNestedServiceLayout(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "app", "service", "sql"), 0755); err != nil {
		t.Fatalf("mkdir service sql dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "app", "service", "wire"), 0755); err != nil {
		t.Fatalf("mkdir service wire dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "app", "service", "sql", "sqlc.yaml"), []byte(`version: "2"
sql:
  - gen:
      go:
        out: ../zgen/querier
`), 0644); err != nil {
		t.Fatalf("write sqlc config: %v", err)
	}
	config := &Config{
		Schemas: &SchemasConfig{
			Path:   "api/schemas",
			Output: "pkg/zgen/schemas",
		},
		OapiCodegen: []OapiCodegenConfig{{
			Path: "app/service/api/openapi", Out: "app/service/zgen/apigen/spec_gen.go", Package: "apigen",
		}},
		TaskHandler: []TaskHandlerConfig{{
			Path: "app/service/api/tasks.yaml", Out: "app/service/zgen/taskgen/runner_gen.go", Package: "taskgen",
		}},
		DST: []DSTConfig{{
			Path: "app/service/api/store.yaml", Out: "app/service/zgen/store_gen.go",
		}},
		CleanItems: []string{"app/*/zgen/*.go"},
		AnclaxDef:  "app/service/anclaxdef",
		Mockgen: &MockgenConfig{Files: []MockgenFileConfig{{
			Source: "app/service/service.go", Destination: "app/service/mock_gen.go", Package: "service",
		}}},
		Wire: []WireConfig{{Path: "./app/service/wire"}},
		Sqlc: []SqlcConfig{{Path: "app/service/sql/sqlc.yaml"}},
	}
	if err := validateCodegenPaths(workdir, config); err != nil {
		t.Fatalf("validateCodegenPaths() rejected nested service layout: %v", err)
	}
}

func TestCodegenValidatesAllPathsBeforeClean(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(workdir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	generated := filepath.Join(workdir, "generated.go")
	if err := os.WriteFile(generated, []byte("generated"), 0644); err != nil {
		t.Fatalf("write generated file: %v", err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "anclax.yaml"), []byte(`clean:
  - generated.go
anclaxdef: ../outside
`), 0644); err != nil {
		t.Fatalf("write anclax config: %v", err)
	}

	if err := codegen("anclax.yaml", workdir); err == nil {
		t.Fatal("codegen() unexpectedly accepted an escaping output")
	}
	if _, err := os.Stat(generated); err != nil {
		t.Fatalf("generated file was cleaned before validation: %v", err)
	}
	assertCodegenSentinel(t, sentinel)
}

func TestDirectGeneratorsRejectEscapingMutationTargets(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(workdir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	externalInput := filepath.Join(outside, "input.yaml")

	for name, output := range map[string]string{
		"absolute": sentinel,
		"lexical":  filepath.Join("..", "outside", "sentinel"),
		"symlink":  filepath.Join("escape", "sentinel"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := genDST(workdir, &DSTConfig{Path: externalInput, Out: output}); err == nil {
				t.Fatal("genDST() unexpectedly accepted an escaping output")
			}
			if err := writeAnclaxDef(workdir, output); err == nil {
				t.Fatal("writeAnclaxDef() unexpectedly accepted an escaping output")
			}
			if err := genMock(workdir, &MockgenConfig{Files: []MockgenFileConfig{{
				Source: externalInput, Destination: output, Package: "mockgen",
			}}}); err == nil {
				t.Fatal("genMock() unexpectedly accepted an escaping destination")
			}
			assertCodegenSentinel(t, sentinel)
		})
	}
}

func TestCleanRejectsEscapesAndRestoresNestedServiceFiles(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(workdir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	for name, pattern := range map[string]string{
		"absolute": sentinel,
		"lexical":  filepath.Join("..", "outside", "sentinel"),
		"symlink":  filepath.Join("escape", "sentinel"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := clean(newCodegenTestStaging(t, workdir), &Config{CleanItems: []string{pattern}}, workdir); err == nil {
				t.Fatal("clean() unexpectedly accepted an escaping pattern")
			}
			assertCodegenSentinel(t, sentinel)
		})
	}
	internalSentinel := filepath.Join(workdir, "aaa", "sentinel")
	if err := os.MkdirAll(filepath.Dir(internalSentinel), 0755); err != nil {
		t.Fatalf("mkdir internal sentinel dir: %v", err)
	}
	if err := os.WriteFile(internalSentinel, []byte("internal"), 0644); err != nil {
		t.Fatalf("write internal sentinel: %v", err)
	}
	if err := clean(newCodegenTestStaging(t, workdir), &Config{CleanItems: []string{filepath.Join("*", "sentinel")}}, workdir); err == nil {
		t.Fatal("clean() unexpectedly followed a wildcard-matched escaping symlink")
	}
	if raw, err := os.ReadFile(internalSentinel); err != nil || string(raw) != "internal" {
		t.Fatalf("safe match moved before all matches were validated: content = %q, error = %v", raw, err)
	}
	assertCodegenSentinel(t, sentinel)

	generated := filepath.Join(workdir, "app", "service", "zgen", "generated.go")
	if err := os.MkdirAll(filepath.Dir(generated), 0755); err != nil {
		t.Fatalf("mkdir generated dir: %v", err)
	}
	if err := os.WriteFile(generated, []byte("generated"), 0644); err != nil {
		t.Fatalf("write generated file: %v", err)
	}
	tempDir := newCodegenTestStaging(t, workdir)
	config := &Config{CleanItems: []string{filepath.Join("app", "*", "zgen", "*.go")}}
	if err := clean(tempDir, config, workdir); err != nil {
		t.Fatalf("clean() rejected nested service output: %v", err)
	}
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("generated file still present after clean, error = %v", err)
	}
	if err := restore(tempDir, config, workdir); err != nil {
		t.Fatalf("restore() error = %v", err)
	}
	if _, err := os.Stat(generated); err != nil {
		t.Fatalf("restored generated file missing: %v", err)
	}

	overlapFile := filepath.Join(workdir, "overlap", "generated.go")
	if err := os.MkdirAll(filepath.Dir(overlapFile), 0755); err != nil {
		t.Fatalf("mkdir overlap dir: %v", err)
	}
	if err := os.WriteFile(overlapFile, []byte("overlap"), 0644); err != nil {
		t.Fatalf("write overlap file: %v", err)
	}
	overlapTemp := newCodegenTestStaging(t, workdir)
	overlapConfig := &Config{CleanItems: []string{
		filepath.Join("overlap", "*"),
		filepath.Join("overlap", "*.go"),
	}}
	if err := clean(overlapTemp, overlapConfig, workdir); err != nil {
		t.Fatalf("clean() rejected overlapping patterns: %v", err)
	}
	if err := restore(overlapTemp, overlapConfig, workdir); err != nil {
		t.Fatalf("restore() overlapping patterns: %v", err)
	}
	if raw, err := os.ReadFile(overlapFile); err != nil || string(raw) != "overlap" {
		t.Fatalf("overlap file content = %q, error = %v", raw, err)
	}
	ancestorFile := filepath.Join(workdir, "ancestor", "generated.go")
	if err := os.MkdirAll(filepath.Dir(ancestorFile), 0755); err != nil {
		t.Fatalf("mkdir ancestor dir: %v", err)
	}
	if err := os.WriteFile(ancestorFile, []byte("ancestor"), 0644); err != nil {
		t.Fatalf("write ancestor file: %v", err)
	}
	ancestorTemp := newCodegenTestStaging(t, workdir)
	ancestorConfig := &Config{CleanItems: []string{"ancestor", filepath.Join("ancestor", "*.go")}}
	if err := clean(ancestorTemp, ancestorConfig, workdir); err != nil {
		t.Fatalf("clean() rejected ancestor-overlapping patterns: %v", err)
	}
	if err := restore(ancestorTemp, ancestorConfig, workdir); err != nil {
		t.Fatalf("restore() ancestor-overlapping patterns: %v", err)
	}
	if raw, err := os.ReadFile(ancestorFile); err != nil || string(raw) != "ancestor" {
		t.Fatalf("ancestor file content = %q, error = %v", raw, err)
	}
	aliasTarget := filepath.Join(workdir, "alias-target")
	if err := os.MkdirAll(aliasTarget, 0755); err != nil {
		t.Fatalf("mkdir alias target: %v", err)
	}
	aliasFile := filepath.Join(aliasTarget, "generated.go")
	if err := os.WriteFile(aliasFile, []byte("alias"), 0644); err != nil {
		t.Fatalf("write alias file: %v", err)
	}
	if err := os.Symlink(aliasTarget, filepath.Join(workdir, "alias")); err != nil {
		t.Fatalf("create internal clean symlink: %v", err)
	}
	aliasTemp := newCodegenTestStaging(t, workdir)
	aliasConfig := &Config{CleanItems: []string{
		filepath.Join("alias", "*.go"),
		filepath.Join("alias-target", "*.go"),
	}}
	if err := clean(aliasTemp, aliasConfig, workdir); err != nil {
		t.Fatalf("clean() rejected internal-symlink overlap: %v", err)
	}
	if err := restore(aliasTemp, aliasConfig, workdir); err != nil {
		t.Fatalf("restore() internal-symlink overlap: %v", err)
	}
	if raw, err := os.ReadFile(aliasFile); err != nil || string(raw) != "alias" {
		t.Fatalf("alias file content = %q, error = %v", raw, err)
	}
	aliasAncestorTemp := newCodegenTestStaging(t, workdir)
	aliasAncestorConfig := &Config{CleanItems: []string{
		"alias-target",
		filepath.Join("alias", "*.go"),
	}}
	if err := clean(aliasAncestorTemp, aliasAncestorConfig, workdir); err != nil {
		t.Fatalf("clean() rejected canonical ancestor overlap: %v", err)
	}
	if err := restore(aliasAncestorTemp, aliasAncestorConfig, workdir); err != nil {
		t.Fatalf("restore() canonical ancestor overlap: %v", err)
	}
	if raw, err := os.ReadFile(aliasFile); err != nil || string(raw) != "alias" {
		t.Fatalf("canonical ancestor file content = %q, error = %v", raw, err)
	}
	aliasDependencyTemp := newCodegenTestStaging(t, workdir)
	aliasDependencyConfig := &Config{CleanItems: []string{
		filepath.Join("alias-target", "*.go"),
		filepath.Join("alias", "*.go"),
		"alias",
	}}
	if err := clean(aliasDependencyTemp, aliasDependencyConfig, workdir); err != nil {
		t.Fatalf("clean() rejected symlink dependency overlap: %v", err)
	}
	if err := restore(aliasDependencyTemp, aliasDependencyConfig, workdir); err != nil {
		t.Fatalf("restore() symlink dependency overlap: %v", err)
	}
	if raw, err := os.ReadFile(aliasFile); err != nil || string(raw) != "alias" {
		t.Fatalf("symlink dependency file content = %q, error = %v", raw, err)
	}
	shortTarget := filepath.Join(workdir, "a")
	if err := os.MkdirAll(shortTarget, 0755); err != nil {
		t.Fatalf("mkdir short alias target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shortTarget, "generated.go"), []byte("short"), 0644); err != nil {
		t.Fatalf("write short target file: %v", err)
	}
	longAlias := "very-long-alias"
	if err := os.Symlink("a", filepath.Join(workdir, longAlias)); err != nil {
		t.Fatalf("create long internal alias: %v", err)
	}
	longAliasTemp := newCodegenTestStaging(t, workdir)
	longAliasConfig := &Config{CleanItems: []string{"a", longAlias}}
	if err := clean(longAliasTemp, longAliasConfig, workdir); err != nil {
		t.Fatalf("clean() rejected long-alias overlap: %v", err)
	}
	if err := restore(longAliasTemp, longAliasConfig, workdir); err != nil {
		t.Fatalf("restore() long-alias overlap: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(shortTarget, "generated.go")); err != nil || string(raw) != "short" {
		t.Fatalf("long-alias target content = %q, error = %v", raw, err)
	}

	rollbackA := filepath.Join(workdir, "rollback-a")
	rollbackB := filepath.Join(workdir, "rollback-b")
	if err := os.WriteFile(rollbackA, []byte("a"), 0644); err != nil {
		t.Fatalf("write rollback-a: %v", err)
	}
	if err := os.WriteFile(rollbackB, []byte("b"), 0644); err != nil {
		t.Fatalf("write rollback-b: %v", err)
	}
	rollbackTemp := newCodegenTestStaging(t, workdir)
	if err := os.MkdirAll(filepath.Join(workdir, rollbackTemp, "rollback-b"), 0755); err != nil {
		t.Fatalf("create conflicting rollback target: %v", err)
	}
	if err := clean(rollbackTemp, &Config{CleanItems: []string{"rollback-*"}}, workdir); err == nil {
		t.Fatal("clean() unexpectedly succeeded with a conflicting temporary target")
	}
	for path, want := range map[string]string{rollbackA: "a", rollbackB: "b"} {
		if raw, err := os.ReadFile(path); err != nil || string(raw) != want {
			t.Fatalf("rollback file %s content = %q, error = %v", path, raw, err)
		}
	}
}

func TestValidateSQLCOutputsConstrainsPathsRelativeToConfig(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	configDir := filepath.Join(workdir, "app", "service", "sql")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	configPath := filepath.Join("app", "service", "sql", "sqlc.yaml")

	for name, output := range map[string]string{
		"absolute": filepath.Join(outside, "generated"),
		"lexical":  filepath.Join("..", "..", "..", "..", "outside", "generated"),
		"symlink":  filepath.Join("..", "..", "..", "escape", "generated"),
	} {
		t.Run(name, func(t *testing.T) {
			writeSQLCConfig(t, filepath.Join(workdir, configPath), output)
			if err := validateSQLCOutputs(workdir, configPath); err == nil {
				t.Fatal("validateSQLCOutputs() unexpectedly accepted an escaping output")
			}
		})
	}
	aliasConfig := `version: "2"
options:
  unsafe: &unsafe ../../../../outside/generated
sql:
  - gen:
      go:
        out: *unsafe
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(aliasConfig), 0644); err != nil {
		t.Fatalf("write aliased sqlc config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted an aliased escaping output")
	}
	keyAliasConfig := `version: "2"
options:
  output_key: &outputKey out
sql:
  - gen:
      go:
        *outputKey: ../../../../outside/generated
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(keyAliasConfig), 0644); err != nil {
		t.Fatalf("write key-aliased sqlc config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted a key-aliased escaping output")
	}
	binaryValueConfig := `version: "2"
sql:
  - gen:
      go:
        out: !!binary Li4vLi4vLi4vLi4vb3V0c2lkZS9nZW5lcmF0ZWQ=
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(binaryValueConfig), 0644); err != nil {
		t.Fatalf("write binary-valued sqlc config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted a binary-tagged escaping output")
	}
	binaryKeyConfig := `version: "2"
sql:
  - gen:
      go:
        !!binary b3V0: ../../../../outside/generated
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(binaryKeyConfig), 0644); err != nil {
		t.Fatalf("write binary-keyed sqlc config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted a binary-tagged output key")
	}
	v1Config := `version: "1"
packages:
  - name: querier
    path: ../../../../outside/generated
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(v1Config), 0644); err != nil {
		t.Fatalf("write sqlc v1 config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted a sqlc v1 escaping package path")
	}
	customCodegenConfig := `version: "2"
plugins:
  - name: trusted
    process:
      cmd: trusted-sqlc-plugin
cloud:
  project: project-id
sql:
  - engine: postgresql
    schema: schema.sql
    queries: query.sql
    codegen:
      - plugin: trusted
        out: ../../../../outside/generated
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(customCodegenConfig), 0644); err != nil {
		t.Fatalf("write custom codegen config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted an escaping custom codegen output")
	}
	customCodegenConfig = `version: "2"
plugins:
  - name: trusted
    process:
      cmd: trusted-sqlc-plugin
cloud:
  project: project-id
sql:
  - engine: postgresql
    schema: schema.sql
    queries: query.sql
    codegen:
      - plugin: trusted
        out: ../zgen/plugin
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(customCodegenConfig), 0644); err != nil {
		t.Fatalf("write safe custom codegen config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err != nil {
		t.Fatalf("validateSQLCOutputs() rejected trusted plugin/cloud config: %v", err)
	}
	prefixEscape := filepath.Join(configDir, "gen-escape")
	if err := os.Symlink(outside, prefixEscape); err != nil {
		t.Fatalf("create custom output prefix escape: %v", err)
	}
	customPrefixConfig := `version: "2"
plugins:
  - name: trusted-wasm
    wasm:
      url: file://trusted.wasm
      sha256: deadbeef
sql:
  - engine: postgresql
    schema: schema.sql
    queries: query.sql
    codegen:
      - plugin: trusted-wasm
        out: gen
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(customPrefixConfig), 0644); err != nil {
		t.Fatalf("write custom prefix config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted an escaping output-prefix sibling")
	}
	unsafeFilenameConfig := `version: "2"
sql:
  - gen:
      go:
        out: ../zgen/filename
        output_db_file_name: ../outside.go
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(unsafeFilenameConfig), 0644); err != nil {
		t.Fatalf("write unsafe filename config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted an escaping filename option")
	}
	safeDoubleDotFilenameConfig := `version: "2"
sql:
  - gen:
      go:
        out: ../zgen/filename
        output_db_file_name: models..go
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(safeDoubleDotFilenameConfig), 0644); err != nil {
		t.Fatalf("write double-dot filename config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err != nil {
		t.Fatalf("validateSQLCOutputs() rejected safe single-component filename: %v", err)
	}
	safeNestedFilenameConfig := `version: "2"
sql:
  - gen:
      go:
        out: ../zgen/filename
        output_db_file_name: internal/db.go
      json:
        out: ../zgen/json
        filename: artifacts/request.json
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(safeNestedFilenameConfig), 0644); err != nil {
		t.Fatalf("write nested filename config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err != nil {
		t.Fatalf("validateSQLCOutputs() rejected safe nested filenames: %v", err)
	}
	unknownGeneratorConfig := `version: "2"
sql:
  - gen:
      future:
        out: ../../../../outside/generated
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(unknownGeneratorConfig), 0644); err != nil {
		t.Fatalf("write unknown generator config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly ignored an unknown generator")
	}
	mergedOutputConfig := `version: "2"
options:
  defaults: &defaults
    out: ../../../../outside/generated
sql:
  - gen:
      go:
        <<: *defaults
        out: ../zgen/merged
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(mergedOutputConfig), 0644); err != nil {
		t.Fatalf("write merged output config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err != nil {
		t.Fatalf("validateSQLCOutputs() did not honor an explicit safe merge override: %v", err)
	}
	mergedV1EscapeConfig := `version: "1"
packages:
  - &defaults
    name: unsafe-default
    path: ../../../../outside/generated
  - <<: *defaults
    name: querier
`
	if err := os.WriteFile(filepath.Join(workdir, configPath), []byte(mergedV1EscapeConfig), 0644); err != nil {
		t.Fatalf("write merged v1 config: %v", err)
	}
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted a merged v1 escape")
	}
	rootConfigPath := "root-sqlc.yaml"
	writeSQLCConfig(t, filepath.Join(workdir, rootConfigPath), ".")
	if err := validateSQLCOutputs(workdir, rootConfigPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted workdir itself as an output")
	}
	unsafeOutput := filepath.Join(workdir, "app", "service", "zgen", "unsafe")
	if err := os.MkdirAll(unsafeOutput, 0755); err != nil {
		t.Fatalf("mkdir unsafe sqlc output: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "sentinel"), filepath.Join(unsafeOutput, "querier.go")); err != nil {
		t.Fatalf("create escaping generated-file symlink: %v", err)
	}
	writeSQLCConfig(t, filepath.Join(workdir, configPath), filepath.Join("..", "zgen", "unsafe"))
	if err := validateSQLCOutputs(workdir, configPath); err == nil {
		t.Fatal("validateSQLCOutputs() unexpectedly accepted an escaping descendant symlink")
	}

	writeSQLCConfig(t, filepath.Join(workdir, configPath), filepath.Join("..", "zgen", "querier"))
	if err := validateSQLCOutputs(workdir, configPath); err != nil {
		t.Fatalf("validateSQLCOutputs() rejected service-local output: %v", err)
	}
}

func TestValidateWireOutputTreeRejectsEscapingGeneratedFile(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	wireDir := filepath.Join(workdir, "app", "service", "wire")
	if err := os.MkdirAll(wireDir, 0755); err != nil {
		t.Fatalf("mkdir wire dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Symlink(sentinel, filepath.Join(wireDir, "wire_gen.go")); err != nil {
		t.Fatalf("create escaping wire output symlink: %v", err)
	}
	config := &Config{Wire: []WireConfig{{Path: filepath.Join("app", "service", "wire")}}}
	if err := validateCodegenPaths(workdir, config); err == nil {
		t.Fatal("validateCodegenPaths() unexpectedly accepted an escaping wire output symlink")
	}
	assertCodegenSentinel(t, sentinel)
}

func TestValidateSQLCOutputsUsesResolvedConfigDirectory(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	actualDir := filepath.Join(workdir, "actual")
	if err := os.MkdirAll(actualDir, 0755); err != nil {
		t.Fatalf("mkdir actual config dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	writeSQLCConfig(t, filepath.Join(actualDir, "sqlc.yaml"), "zgen")
	if err := os.Symlink(filepath.Join("actual", "sqlc.yaml"), filepath.Join(workdir, "sqlc.yaml")); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(actualDir, "zgen")); err != nil {
		t.Fatalf("create escaping output symlink: %v", err)
	}
	if err := validateSQLCOutputs(workdir, "sqlc.yaml"); err == nil {
		t.Fatal("validateSQLCOutputs() used the lexical config directory instead of the resolved directory")
	}
}

func TestResolveWireDirRequiresConcreteWorkdirDirectory(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	wireDir := filepath.Join(workdir, "app", "service", "wire")
	if err := os.MkdirAll(wireDir, 0755); err != nil {
		t.Fatalf("mkdir wire dir: %v", err)
	}
	resolved, err := resolveWireDir(workdir, filepath.Join("app", "service", "wire"))
	if err != nil {
		t.Fatalf("resolveWireDir() rejected local directory: %v", err)
	}
	if resolved != wireDir {
		t.Fatalf("resolveWireDir() = %q, want %q", resolved, wireDir)
	}
	if _, err := resolveWireDir(workdir, "victim.example/pkg"); err == nil {
		t.Fatal("resolveWireDir() unexpectedly accepted a Go import path")
	}
	if _, err := resolveWireDir(workdir, "./..."); err == nil {
		t.Fatal("resolveWireDir() unexpectedly accepted a package pattern")
	}
}

func TestWriteAnclaxDefRejectsEscapingDescendantSymlink(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(workdir, "defs"), 0755); err != nil {
		t.Fatalf("mkdir defs: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "defs", "api")); err != nil {
		t.Fatalf("create escaping API symlink: %v", err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	if err := writeAnclaxDef(workdir, "defs"); err == nil {
		t.Fatal("writeAnclaxDef() unexpectedly followed an escaping descendant symlink")
	}
	assertCodegenSentinel(t, sentinel)
}

func writeSQLCConfig(t *testing.T, path, output string) {
	t.Helper()
	raw := fmt.Sprintf("version: \"2\"\nsql:\n  - gen:\n      go:\n        out: %q\n", output)
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatalf("write sqlc config: %v", err)
	}
}

func assertCodegenSentinel(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read external sentinel: %v", err)
	}
	if string(raw) != "keep" {
		t.Fatalf("external sentinel = %q, want keep", raw)
	}
}

func newCodegenTestStaging(t *testing.T, workdir string) string {
	t.Helper()
	staging, err := codegenpath.MkdirTemp(workdir, ".anclax-codegen-test-")
	if err != nil {
		t.Fatalf("create codegen staging directory: %v", err)
	}
	t.Cleanup(func() {
		_ = codegenpath.RemoveAll(workdir, staging)
	})
	return staging
}
