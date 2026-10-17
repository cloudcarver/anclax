package codegen

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestGenerateReleaseUniqueTagOnTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, yamlValue, assignment string
		invalid                     bool
	}{
		{name: "omitted"},
		{name: "enabled", yamlValue: "true", assignment: "utils.Ptr(true)"},
		{name: "disabled", yamlValue: "false", assignment: "utils.Ptr(false)"},
		{name: "string", yamlValue: `"true"`, invalid: true},
		{name: "number", yamlValue: "1", invalid: true},
		{name: "null", yamlValue: "null", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := "tasks:\n  - name: syncResource\n"
			if tc.yamlValue != "" {
				spec += "    releaseUniqueTagOnTerminal: " + tc.yamlValue + "\n"
			}
			var data map[string]any
			if err := yaml.Unmarshal([]byte(spec), &data); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			code, err := generateToolInterfaces(dir, "taskgen", filepath.Join(dir, "tasks.yaml"), data, nil)
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "releaseUniqueTagOnTerminal for syncResource must be a boolean") {
					t.Fatalf("expected a boolean validation error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.ParseFile(token.NewFileSet(), "runner_gen.go", code, 0); err != nil {
				t.Fatalf("invalid generated Go: %v", err)
			}
			field := "attributes.ReleaseUniqueTagOnTerminal"
			if tc.assignment == "" {
				if strings.Contains(code, field) {
					t.Fatal("omitted option must preserve the default")
				}
			} else if !strings.Contains(code, field+" = "+tc.assignment) {
				t.Fatalf("missing generated option %s", tc.assignment)
			}
		})
	}
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
