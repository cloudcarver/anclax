package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

func TestBundleOpenAPISpecInternalizesImportedSchemas(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	mustWriteBundleFile(t, filepath.Join(workdir, "api", "openapi", "root.yaml"), `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
servers:
  - url: /api/v1
externalDocs:
  url: https://example.com/docs
x-test:
  enabled: true
tags:
  - name: public
`)
	mustWriteBundleFile(t, filepath.Join(workdir, "api", "openapi", "counter.yaml"), `paths:
  /counter:
    get:
      operationId: getCounter
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: ../schemas/counter.yaml#schemas/Counter
`)
	mustWriteBundleFile(t, filepath.Join(workdir, "api", "schemas", "counter.yaml"), `schemas:
  Counter:
    type: object
    required: [count]
    properties:
      count:
        type: integer
        format: int32
`)

	outputPath := filepath.Join("test", "openapi-bundle.yaml")
	if err := bundleOpenAPISpec(workdir, filepath.Join("api", "openapi"), outputPath); err != nil {
		t.Fatalf("bundleOpenAPISpec: %v", err)
	}

	fullOutputPath := filepath.Join(workdir, outputPath)
	raw, err := os.ReadFile(fullOutputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(raw)
	if !strings.HasPrefix(out, "# DO NOT EDIT. Generated Code. The single source of truth is api/openapi\n") {
		t.Fatalf("bundled spec missing generated header: %q", out)
	}
	if strings.Contains(out, "../schemas/") {
		t.Fatalf("bundled spec still contains external schema refs: %s", out)
	}
	if !strings.Contains(out, "#/components/schemas/Counter") {
		t.Fatalf("bundled spec missing internalized schema ref: %s", out)
	}
	if !strings.Contains(out, "\n  title: test\n") {
		t.Fatalf("bundled spec missing 2-space indentation for nested fields: %q", out)
	}
	if strings.Contains(out, "\n    title: test\n") {
		t.Fatalf("bundled spec unexpectedly uses 4-space indentation for nested fields: %q", out)
	}
	if got, want := topLevelKeys(t, raw), []string{"openapi", "info", "servers", "paths", "components", "x-test", "externalDocs", "tags"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level key order = %v, want %v", got, want)
	}

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(fullOutputPath)
	if err != nil {
		t.Fatalf("load bundled file: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate bundled file: %v", err)
	}
	if doc.Components == nil || doc.Components.Schemas == nil || doc.Components.Schemas["Counter"] == nil {
		t.Fatalf("bundled doc missing Counter schema in components")
	}
}

func TestBundleOpenAPISpecRejectsOutputInsideInputDirectory(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	mustWriteBundleFile(t, filepath.Join(workdir, "api", "openapi", "root.yaml"), `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
`)

	err := bundleOpenAPISpec(workdir, filepath.Join("api", "openapi"), filepath.Join("api", "openapi", "bundle.yaml"))
	if err == nil {
		t.Fatal("expected error when output is inside input directory")
	}
	if !strings.Contains(err.Error(), "must be outside OpenAPI input directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func topLevelKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		t.Fatalf("unmarshal yaml node: %v", err)
	}
	if len(node.Content) == 0 || node.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("unexpected yaml root node: %+v", node)
	}
	mapping := node.Content[0]
	keys := make([]string, 0, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		keys = append(keys, mapping.Content[i].Value)
	}
	return keys
}

func mustWriteBundleFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
