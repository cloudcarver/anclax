package jsondecode_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	oapi "github.com/cloudcarver/anclax/pkg/codegen/oapi"
	"github.com/cloudcarver/anclax/pkg/codegen/schemas"
	task "github.com/cloudcarver/anclax/pkg/codegen/task"
	"gopkg.in/yaml.v3"
)

func TestGeneratedInt64Decoders(t *testing.T) {
	workdir := t.TempDir()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	module := strings.Replace(read(t, filepath.Join(root, "go.mod")), "module github.com/cloudcarver/anclax", "module example.com/decodertest", 1)
	module += "\nrequire github.com/cloudcarver/anclax v0.0.0\nreplace github.com/cloudcarver/anclax => " + strconv.Quote(root) + "\n"
	write(t, workdir, "go.mod", module)
	write(t, workdir, "go.sum", read(t, filepath.Join(root, "go.sum")))
	models := read(t, "testdata/models.yaml")
	write(t, workdir, "api/models.yaml", models)
	config := &schemas.Config{Path: "api", Output: "shared"}
	manager, err := schemas.Load(workdir, *config)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Generate(); err != nil {
		t.Fatal(err)
	}

	var modelDoc map[string]any
	if err := yaml.Unmarshal([]byte(models), &modelDoc); err != nil {
		t.Fatal(err)
	}
	modelMap := modelDoc["schemas"].(map[string]any)
	oapiModels := strings.ReplaceAll(models, "#/schemas/", "#/components/schemas/")
	var components map[string]any
	if err := yaml.Unmarshal([]byte(oapiModels), &components); err != nil {
		t.Fatal(err)
	}
	components["schemas"].(map[string]any)["ExternalRequest"] = map[string]any{
		"type": "object", "properties": map[string]any{
			"request": map[string]any{"$ref": "./models.yaml#/schemas/Request"},
		},
	}
	spec := map[string]any{
		"openapi": "3.0.3", "info": map[string]any{"title": "decoder test", "version": "1"},
		"components": components,
		"paths": map[string]any{"/devices": map[string]any{"post": map[string]any{
			"operationId": "CreateDevice",
			"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Request"}}}},
			"responses":   map[string]any{"200": map[string]any{"description": "ok"}},
		}}},
	}
	writeYAML(t, workdir, "api/openapi.yaml", spec)
	if err := oapi.Generate(workdir, oapi.Config{Path: "api/openapi.yaml", Out: "apigen/spec.go", Package: "apigen", Schemas: config}); err != nil {
		t.Fatal(err)
	}

	// Task inline objects have their own generated types and decoder methods.
	params := modelMap["Request"].(map[string]any)
	params["properties"].(map[string]any)["inline_child"] = modelMap["Child"]
	tasks := map[string]any{"tasks": []any{
		map[string]any{"name": "submit", "parameters": params},
		map[string]any{"name": "scalar", "parameters": modelMap["Key"]},
		map[string]any{"name": "array", "parameters": modelMap["Keys"]},
	}}
	data, err := yaml.Marshal(tasks)
	if err != nil {
		t.Fatal(err)
	}
	write(t, workdir, "api/tasks.yaml", strings.ReplaceAll(string(data), "#/schemas/", "./models.yaml#/schemas/"))
	write(t, workdir, "taskgen/runner.go", "")
	if err := task.Generate(workdir, "taskgen", "api/tasks.yaml", "taskgen/runner.go", config); err != nil {
		t.Fatal(err)
	}
	write(t, workdir, "decoder_test.go", read(t, "testdata/decoder_test.go.txt"))
	cmd := exec.Command("go", "test", "-mod=mod", "-count=1", "./...")
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated decoder tests failed: %v\n%s", err, output)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func write(t *testing.T, dir, path, data string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeYAML(t *testing.T, dir, path string, value any) {
	t.Helper()
	data, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, path, string(data))
}
