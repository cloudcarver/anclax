package schemas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratePrefixesEnumValuesWithSchemaName(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "go.mod"), []byte("module example.com/test\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	schemaDir := filepath.Join(workdir, "api", "schemas")
	if err := os.MkdirAll(schemaDir, 0755); err != nil {
		t.Fatalf("mkdir schema dir: %v", err)
	}

	schema := `schemas:
  systemEvent:
    type: string
    enum:
      - Cancel
`
	if err := os.WriteFile(filepath.Join(schemaDir, "events.yaml"), []byte(schema), 0644); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	manager, err := Load(workdir, Config{
		Path:   "api/schemas",
		Output: "pkg/zgen/schemas",
	})
	if err != nil {
		t.Fatalf("load manager: %v", err)
	}
	if manager == nil {
		t.Fatal("expected manager")
	}
	if err := manager.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}

	outPath := filepath.Join(workdir, "pkg", "zgen", "schemas", "events.go")
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	out := string(raw)

	required := []string{
		"type SystemEvent string",
		"SystemEventCancel SystemEvent = \"Cancel\"",
	}
	for _, needle := range required {
		if !strings.Contains(out, needle) {
			t.Fatalf("generated output missing %q", needle)
		}
	}

	forbidden := []string{
		"\tCancel SystemEvent = \"Cancel\"\n",
	}
	for _, needle := range forbidden {
		if strings.Contains(out, needle) {
			t.Fatalf("generated output unexpectedly contains %q", needle)
		}
	}
}

func TestLoadRejectsSchemaOutputsOutsideWorkdir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "absolute"},
		{name: "lexical escape"},
		{name: "symlink parent escape"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			workdir := filepath.Join(parent, "repo")
			outside := filepath.Join(parent, "outside")
			if err := os.MkdirAll(filepath.Join(workdir, "api", "schemas"), 0755); err != nil {
				t.Fatalf("mkdir schemas: %v", err)
			}
			if err := os.MkdirAll(outside, 0755); err != nil {
				t.Fatalf("mkdir outside: %v", err)
			}
			if err := os.WriteFile(filepath.Join(workdir, "go.mod"), []byte("module example.com/test\n"), 0644); err != nil {
				t.Fatalf("write go.mod: %v", err)
			}
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
				t.Fatalf("write sentinel: %v", err)
			}
			output := outside
			switch tt.name {
			case "lexical escape":
				output = filepath.Join("..", "outside")
			case "symlink parent escape":
				if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
					t.Fatalf("create escaping symlink: %v", err)
				}
				output = filepath.Join("escape", "generated")
			}

			manager, err := Load(workdir, Config{
				Path:   filepath.Join("api", "schemas"),
				Output: output,
			})
			if err == nil {
				if manager != nil {
					_ = manager.Generate()
				}
				t.Fatal("Load() unexpectedly accepted an output outside workdir")
			}
			raw, readErr := os.ReadFile(sentinel)
			if readErr != nil {
				t.Fatalf("read sentinel: %v", readErr)
			}
			if string(raw) != "keep" {
				t.Fatalf("sentinel = %q, want keep", raw)
			}
		})
	}
}

func TestGenerateSupportsNestedServiceSchemaOutput(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "go.mod"), []byte("module example.com/test\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mustWriteSchemaFile(t, filepath.Join(workdir, "app", "service", "api", "schemas", "event.yaml"), `schemas:
  Event:
    type: object
`)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	relativeWorkdir, err := filepath.Rel(cwd, workdir)
	if err != nil {
		t.Fatalf("make workdir relative: %v", err)
	}
	manager, err := Load(relativeWorkdir, Config{
		Path:   filepath.Join("app", "service", "api", "schemas"),
		Output: filepath.Join("app", "service", "zgen", "schemas"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := manager.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "app", "service", "zgen", "schemas", "event.go")); err != nil {
		t.Fatalf("stat nested generated schema: %v", err)
	}
}

func TestGenerateUsesLexicalImportPathForInternalOutputSymlink(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "go.mod"), []byte("module example.com/test\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mustWriteSchemaFile(t, filepath.Join(workdir, "api", "schemas", "child", "event.yaml"), `schemas:
  Event:
    type: object
`)
	actualOutput := filepath.Join(workdir, "actual")
	if err := os.MkdirAll(actualOutput, 0755); err != nil {
		t.Fatalf("mkdir actual output: %v", err)
	}
	if err := os.Symlink(actualOutput, filepath.Join(workdir, "gen")); err != nil {
		t.Fatalf("create internal output symlink: %v", err)
	}

	manager, err := Load(workdir, Config{Path: filepath.Join("api", "schemas"), Output: "gen"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, file := range manager.files {
		if file.ImportPath != "example.com/test/gen/child" {
			t.Fatalf("schema import path = %q, want lexical output path", file.ImportPath)
		}
	}
	if err := manager.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "gen", "child", "event.go")); err != nil {
		t.Fatalf("stat generated schema: %v", err)
	}
	if _, err := os.Stat(filepath.Join(actualOutput, "child", "event.go")); !os.IsNotExist(err) {
		t.Fatalf("generated schema unexpectedly used symlink target, error = %v", err)
	}
}

func mustWriteSchemaFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
