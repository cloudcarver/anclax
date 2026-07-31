package codegen

import (
	"go/parser"
	"go/token"
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
