package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
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

func TestGenerateToolInterfacesQuotesTaskStrings(t *testing.T) {
	workdir := t.TempDir()
	payload := "value\"\nfunc Injected() {}\n//"
	propertyName := "fieldName"
	data := map[string]any{
		"tasks": []any{
			map[string]any{
				"name": "safeTask",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						propertyName: map[string]any{"type": "string"},
					},
				},
				"cronjob": map[string]any{"cronExpression": payload},
				"retryPolicy": map[string]any{
					"interval":    payload,
					"maxAttempts": 3,
				},
				"labels": []any{payload},
				"tags":   []any{payload},
			},
		},
	}

	code, err := generateToolInterfaces(workdir, "taskgen", filepath.Join(workdir, "tasks.yaml"), data, nil)
	if err != nil {
		t.Fatalf("generateToolInterfaces: %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "runner_gen.go", code, parser.AllErrors)
	if err != nil {
		t.Fatalf("generated code is not valid Go: %v\n%s", err, code)
	}
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == "Injected" {
			t.Fatal("task string injected a function declaration")
		}
	}
	if got := strings.Count(code, strconv.Quote(payload)); got < 4 {
		t.Fatalf("quoted payload appears %d times, want at least 4\n%s", got, code)
	}
}

func TestGenerateToolInterfacesRejectsMaliciousIdentifiersAndTypes(t *testing.T) {
	workdir := t.TempDir()
	for _, test := range []struct {
		name      string
		data      map[string]any
		wantError string
	}{
		{
			name: "task name",
			data: map[string]any{"tasks": []any{map[string]any{
				"name": "safeTask\nfunc Injected() {}",
			}}},
			wantError: "invalid task name",
		},
		{
			name: "integer format",
			data: map[string]any{"tasks": []any{map[string]any{
				"name": "safeTask",
				"parameters": map[string]any{
					"type":   "integer",
					"format": "int32; func Injected() {}",
				},
			}}},
			wantError: "unsupported integer format",
		},
		{
			name: "custom type",
			data: map[string]any{"tasks": []any{map[string]any{
				"name": "safeTask",
				"parameters": map[string]any{
					"x-go-type": "int; func Injected() {}",
				},
			}}},
			wantError: "invalid x-go-type",
		},
		{
			name: "field name",
			data: map[string]any{"tasks": []any{map[string]any{
				"name": "safeTask",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"field` json:\"attacker\"": map[string]any{"type": "string"},
					},
				},
			}}},
			wantError: "invalid generated field name",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := generateToolInterfaces(workdir, "taskgen", filepath.Join(workdir, "tasks.yaml"), test.data, nil)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}
