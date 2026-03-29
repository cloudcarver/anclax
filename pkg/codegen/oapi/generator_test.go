package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
