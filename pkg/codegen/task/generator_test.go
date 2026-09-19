package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateToolInterfacesEmitsTaskConstructorHelpers(t *testing.T) {
	workdir := t.TempDir()
	data := map[string]any{
		"tasks": []any{
			map[string]any{
				"name":        "sendEmail",
				"description": "Send an email",
				"delay":       "5m",
				"parameters": map[string]any{
					"type":     "object",
					"required": []any{"userID"},
					"properties": map[string]any{
						"userID": map[string]any{
							"type":   "integer",
							"format": "int64",
						},
					},
				},
				"retryPolicy": map[string]any{
					"interval":    "30m",
					"maxAttempts": 3,
				},
			},
		},
	}

	code, err := generateToolInterfaces(workdir, "taskgen", filepath.Join(workdir, "tasks.yaml"), data, nil)
	if err != nil {
		t.Fatalf("generateToolInterfaces() error = %v", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "runner_gen.go", code, 0); err != nil {
		t.Fatalf("generated code is not valid Go: %v\n%s", err, code)
	}

	assertContains := func(needle string) {
		t.Helper()
		if !strings.Contains(code, needle) {
			t.Fatalf("generated code missing %q\n%s", needle, code)
		}
	}

	assertContains("task, err := NewSendEmailTask(params, overrides...)")
	assertContains("func NewSendEmailTask(params *SendEmailParameters, overrides ...taskcore.TaskOverride) (*apigen.Task, error) {")
	assertContains("task.StartedAt = utils.Ptr(time.Now().Add(delay))")
	assertContains("return nil, errors.Wrap(err, \"failed to apply task override\")")

	assertNotContains := func(needle string) {
		t.Helper()
		if strings.Contains(code, needle) {
			t.Fatalf("generated code unexpectedly contains %q\n%s", needle, code)
		}
	}
	assertNotContains("now func() time.Time")
	assertNotContains("c.now")
	assertNotContains("newSendEmailTask")
}

func TestGenerateToolInterfacesOmitsTimeImportWithoutDelay(t *testing.T) {
	workdir := t.TempDir()
	data := map[string]any{
		"tasks": []any{
			map[string]any{
				"name":        "sendEmail",
				"description": "Send an email",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
	}

	code, err := generateToolInterfaces(workdir, "taskgen", filepath.Join(workdir, "tasks.yaml"), data, nil)
	if err != nil {
		t.Fatalf("generateToolInterfaces() error = %v", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "runner_gen.go", code, 0); err != nil {
		t.Fatalf("generated code is not valid Go: %v\n%s", err, code)
	}
	if strings.Contains(code, "\n\t\"time\"\n") {
		t.Fatalf("generated code imports time without delay\n%s", code)
	}
}

func TestGenerateConstrainsTaskOutputToWorkdir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		out  func(sentinel string) string
	}{
		{name: "absolute", out: func(sentinel string) string { return sentinel }},
		{name: "lexical escape", out: func(string) string { return filepath.Join("..", "outside", "sentinel") }},
		{name: "symlink parent escape", out: func(string) string { return filepath.Join("escape", "sentinel") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			workdir := filepath.Join(parent, "repo")
			outside := filepath.Join(parent, "outside")
			if err := os.MkdirAll(workdir, 0755); err != nil {
				t.Fatalf("mkdir workdir: %v", err)
			}
			if err := os.MkdirAll(outside, 0755); err != nil {
				t.Fatalf("mkdir outside: %v", err)
			}
			if tt.name == "symlink parent escape" {
				if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
					t.Fatalf("create escaping symlink: %v", err)
				}
			}
			if err := os.WriteFile(filepath.Join(workdir, "tasks.yaml"), []byte(minimalTaskSpec), 0644); err != nil {
				t.Fatalf("write task spec: %v", err)
			}
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
				t.Fatalf("write sentinel: %v", err)
			}

			err := Generate(workdir, "taskgen", "tasks.yaml", tt.out(sentinel), nil)
			if err == nil {
				t.Fatal("Generate() unexpectedly accepted an output outside workdir")
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

func TestGenerateSupportsNestedServiceTaskOutput(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "app", "service", "api"), 0755); err != nil {
		t.Fatalf("mkdir task spec dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "app", "service", "api", "tasks.yaml"), []byte(minimalTaskSpec), 0644); err != nil {
		t.Fatalf("write task spec: %v", err)
	}
	if err := Generate(
		workdir,
		"taskgen",
		filepath.Join("app", "service", "api", "tasks.yaml"),
		filepath.Join("app", "service", "zgen", "runner_gen.go"),
		nil,
	); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "app", "service", "zgen", "runner_gen.go")); err != nil {
		t.Fatalf("stat generated task file: %v", err)
	}
}

const minimalTaskSpec = `tasks:
  - name: ping
    description: Ping
    parameters:
      type: object
      properties: {}
`
