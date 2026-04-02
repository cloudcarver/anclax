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
