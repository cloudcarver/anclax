package schemas

import (
	"go/parser"
	"go/token"
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

func TestGenerateRejectsMaliciousSchemaTypes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		schema    string
		wantError string
	}{
		{
			name: "integer format",
			schema: `schemas:
  Payload:
    type: integer
    format: "int32; func Injected() {}"
`,
			wantError: "unsupported integer format",
		},
		{
			name: "custom type",
			schema: `schemas:
  Payload:
    type: string
    x-go-type: "json.RawMessage; func Injected() {}"
`,
			wantError: "invalid x-go-type",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			workdir := t.TempDir()
			mustWriteSchemaTestFile(t, filepath.Join(workdir, "go.mod"), "module example.com/test\n")
			mustWriteSchemaTestFile(t, filepath.Join(workdir, "api", "schemas", "payload.yaml"), test.schema)
			manager, err := Load(workdir, Config{Path: "api/schemas", Output: "pkg/zgen/schemas"})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			err = manager.Generate()
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Generate error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestGenerateEscapesBackticksInSchemaTags(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	mustWriteSchemaTestFile(t, filepath.Join(workdir, "go.mod"), "module example.com/test\n")
	mustWriteSchemaTestFile(t, filepath.Join(workdir, "api", "schemas", "payload.yaml"), `schemas:
  Payload:
    type: object
    properties:
      'value`+"`"+` json:"attacker"':
        type: string
`)
	manager, err := Load(workdir, Config{Path: "api/schemas", Output: "pkg/zgen/schemas"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := manager.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(workdir, "pkg", "zgen", "schemas", "payload.go"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "payload.go", raw, parser.AllErrors); err != nil {
		t.Fatalf("generated output is not valid Go: %v\n%s", err, raw)
	}
	if strings.Contains(string(raw), "`json:") {
		t.Fatalf("generated output uses a raw struct tag: %s", raw)
	}
}

func mustWriteSchemaTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}
