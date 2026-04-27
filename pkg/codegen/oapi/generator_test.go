package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	schema_codegen "github.com/cloudcarver/anclax/pkg/codegen/schemas"
)

func TestGenerateHandlesQueryParamsEnumsAndUUIDPaths(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	specPath := filepath.Join(workdir, "spec.yaml")
	outPath := filepath.Join(workdir, "spec_gen.go")

	spec := `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths:
  /memos:
    get:
      operationId: ListMemos
      summary: List memos
      parameters:
        - in: query
          name: q
          schema:
            type: string
        - in: query
          name: state
          schema:
            $ref: '#/components/schemas/MemoState'
        - in: query
          name: limit
          schema:
            type: integer
            format: int32
      responses:
        '200':
          description: ok
  /memos/{id}:
    get:
      operationId: GetMemo
      summary: Get memo
      parameters:
        - in: path
          name: id
          required: true
          schema:
            type: string
            format: uuid
      responses:
        '200':
          description: ok
components:
  schemas:
    MemoState:
      type: string
      enum:
        - active
        - archived
    TodoItemBucket:
      type: string
      enum:
        - later
        - today
        - week
    UpdateTodoRequestBucket:
      type: string
      enum:
        - later
        - today
        - week
`

	if err := os.WriteFile(specPath, []byte(spec), 0644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	if err := Generate(workdir, Config{
		Path:    specPath,
		Out:     outPath,
		Package: "apigen",
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(raw)

	required := []string{
		"type ListMemosParams struct {",
		"`query:\"q\" json:\"q,omitempty\"`",
		"`query:\"state\" json:\"state,omitempty\"`",
		"`query:\"limit\" json:\"limit,omitempty\"`",
		"ListMemos(c fiber.Ctx, params ListMemosParams) error",
		"func (c *Client) ListMemos(ctx context.Context, params *ListMemosParams, reqEditors ...RequestEditorFn) (*http.Response, error) {",
		"func NewListMemosRequest(server string, params *ListMemosParams) (*http.Request, error) {",
		"parsedId, err := uuid.Parse(idValue)",
		"type MemoState string",
		"MemoStateActive",
		"TodoItemBucketLater",
		"UpdateTodoRequestBucketLater",
	}
	for _, needle := range required {
		if !strings.Contains(out, needle) {
			t.Fatalf("generated output missing %q", needle)
		}
	}

	forbidden := []string{
		"type MemoState MemoState",
		"\n\tLater TodoItemBucket = \"later\"\n",
		"\n\tLater UpdateTodoRequestBucket = \"later\"\n",
		"_ = idValue",
		"func (c *Client) ListMemos(ctx context.Context, reqEditors ...RequestEditorFn) (*http.Response, error) {",
	}
	for _, needle := range forbidden {
		if strings.Contains(out, needle) {
			t.Fatalf("generated output unexpectedly contains %q", needle)
		}
	}
}

func TestGenerateSupportsDirectoryInput(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, "go.mod"), "module example.com/test\n\ngo 1.24\n")
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "root.yaml"), `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
servers:
  - url: /api/v1
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
`)
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "counter.yaml"), `paths:
  /counter:
    get:
      summary: Get Counter
      operationId: getCounter
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: ../schemas/counter/counter.yaml#schemas/Counter
x-check-rules:
  OperationPermit:
    useContext: true
    parameters:
      - name: operationID
        schema:
          type: string
`)
	mustWriteFile(t, filepath.Join(workdir, "api", "schemas", "counter", "counter.yaml"), `schemas:
  Counter:
    type: object
    required: [count]
    properties:
      count:
        type: integer
        format: int32
`)

	outPath := filepath.Join(workdir, "spec_gen.go")
	if err := Generate(workdir, Config{
		Path:    filepath.Join("api", "openapi"),
		Out:     outPath,
		Package: "apigen",
		Schemas: &schema_codegen.Config{Path: filepath.Join("api", "schemas"), Output: filepath.Join("pkg", "zgen", "schemas")},
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(raw)
	for _, needle := range []string{
		`"example.com/test/pkg/zgen/schemas/counter"`,
		"JSON200      *[]counter.Counter",
		"OperationPermit(c fiber.Ctx, operationID string) error",
		"router.Get(options.BaseURL+\"/counter\", wrapper.GetCounter)",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("generated output missing %q", needle)
		}
	}
}

func TestGenerateMiddlewareUsesWrappedFiberErrorStatus(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	outPath := filepath.Join(workdir, "spec_gen.go")

	if err := Generate(".", Config{
		Path:    filepath.Join("testdata", "x_check_rules_status.yaml"),
		Out:     outPath,
		Package: "apigen",
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(raw)

	required := []string{
		`"errors"`,
		"func xCheckRuleStatusCode(err error) int {",
		"if errors.As(err, &fiberErr) {",
		"return fiberErr.Code",
		"return fiber.StatusForbidden",
		"OperationPermit(c fiber.Ctx, operationID string) error",
	}
	for _, needle := range required {
		if !strings.Contains(out, needle) {
			t.Fatalf("generated output missing %q", needle)
		}
	}

	statusCall := "return c.Status(xCheckRuleStatusCode(err)).SendString(err.Error())"
	if got := strings.Count(out, statusCall); got != 3 {
		t.Fatalf("generated output contains %q %d times, want 3", statusCall, got)
	}
	if strings.Contains(out, "return c.Status(fiber.StatusForbidden).SendString(err.Error())") {
		t.Fatal("generated output still returns fixed 403 for check-rule errors")
	}
}

func TestGenerateSupportsMultilineEnumDescriptions(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	specPath := filepath.Join(workdir, "spec.yaml")
	outPath := filepath.Join(workdir, "spec_gen.go")

	spec := `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths:
  /nodes:
    get:
      operationId: ListNodes
      summary: List nodes
      responses:
        '200':
          description: ok
components:
  schemas:
    NodeStatus:
      type: string
      description: |
        Status of the node.
        - draining: node is shutting down.
        - standby: node can return to service later.
      enum:
        - ready
        - draining
        - standby
`

	if err := os.WriteFile(specPath, []byte(spec), 0644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	if err := Generate(workdir, Config{
		Path:    specPath,
		Out:     outPath,
		Package: "apigen",
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(raw)

	required := []string{
		"// NodeStatus Status of the node.",
		"// - draining: node is shutting down.",
		"// - standby: node can return to service later.",
		"type NodeStatus string",
	}
	for _, needle := range required {
		if !strings.Contains(out, needle) {
			t.Fatalf("generated output missing %q", needle)
		}
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
