package codegen

import (
	"os"
	"os/exec"
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

func TestBuildDocumentResolvesEffectiveSecurity(t *testing.T) {
	t.Parallel()

	workdir, specPath := writeEffectiveSecuritySpec(t)
	spec, sourcePath, err := loadSwagger(workdir, specPath)
	if err != nil {
		t.Fatalf("load OpenAPI spec: %v", err)
	}
	schemaManager, err := schema_codegen.Load(workdir, schema_codegen.Config{})
	if err != nil {
		t.Fatalf("load schema manager: %v", err)
	}
	doc, err := buildDocument(spec, sourcePath, "securitytest", schemaManager)
	if err != nil {
		t.Fatalf("build document: %v", err)
	}

	operations := make(map[string]operationDef, len(doc.Operations))
	for _, operation := range doc.Operations {
		operations[operation.Name] = operation
	}

	inherited := operations["Inherited"]
	if !inherited.NeedsAuth {
		t.Fatal("inherited top-level security did not require authentication")
	}
	if got := len(inherited.SecurityAlternatives); got != 2 {
		t.Fatalf("inherited alternatives = %d, want 2", got)
	}
	if got := len(inherited.SecurityAlternatives[0].Schemes); got != 2 {
		t.Fatalf("first inherited alternative schemes = %d, want 2", got)
	}
	if got := inherited.SecurityAlternatives[0].Schemes[0].ConstName; got != "ApiKeyAuthScopes" {
		t.Fatalf("first sorted inherited scheme = %q, want ApiKeyAuthScopes", got)
	}
	if got := inherited.SecurityAlternatives[0].Schemes[1].ConstName; got != "BearerAuthScopes" {
		t.Fatalf("second sorted inherited scheme = %q, want BearerAuthScopes", got)
	}
	if got := inherited.SecurityAlternatives[1].Schemes[0].ConstName; got != "CookieAuthScopes" {
		t.Fatalf("second inherited alternative scheme = %q, want CookieAuthScopes", got)
	}
	if got := inherited.SecurityAlternatives[0].Schemes[1].Scopes; len(got) != 1 || got[0] != "x.OperationPermit(c, operationID)" {
		t.Fatalf("inherited x-check scopes = %#v", got)
	}

	if public := operations["Public"]; public.NeedsAuth || len(public.SecurityAlternatives) != 0 {
		t.Fatalf("explicit empty security should be anonymous: %#v", public.SecurityAlternatives)
	}
	if anonymous := operations["Anonymous"]; anonymous.NeedsAuth || len(anonymous.SecurityAlternatives) != 1 || len(anonymous.SecurityAlternatives[0].Schemes) != 0 {
		t.Fatalf("empty security requirement should be anonymous: %#v", anonymous.SecurityAlternatives)
	}
	if optional := operations["Optional"]; optional.NeedsAuth || len(optional.SecurityAlternatives) != 2 || len(optional.SecurityAlternatives[0].Schemes) != 0 {
		t.Fatalf("anonymous alternative should make security optional: %#v", optional.SecurityAlternatives)
	}

	override := operations["Override"]
	if !override.NeedsAuth || len(override.SecurityAlternatives) != 1 || len(override.SecurityAlternatives[0].Schemes) != 1 {
		t.Fatalf("operation security override was not normalized: %#v", override.SecurityAlternatives)
	}
	if got := override.SecurityAlternatives[0].Schemes[0].ConstName; got != "ApiKeyAuthScopes" {
		t.Fatalf("override scheme = %q, want ApiKeyAuthScopes", got)
	}
	if got := override.SecurityAlternatives[0].Schemes[0].Scopes; len(got) != 1 || got[0] != "x.OverridePermit(c)" {
		t.Fatalf("override x-check scopes = %#v", got)
	}
}

func TestGeneratedEffectiveSecurityFiberBehavior(t *testing.T) {
	workdir, specPath := writeEffectiveSecuritySpec(t)
	outPath := filepath.Join(workdir, "spec_gen.go")
	if err := Generate(workdir, Config{
		Path:    specPath,
		Out:     outPath,
		Package: "securitytest",
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	mustWriteFile(t, filepath.Join(workdir, "go.mod"), `module example.com/securitytest

go 1.24.0

require github.com/gofiber/fiber/v3 v3.3.0
`)
	mustWriteFile(t, filepath.Join(workdir, "security_test.go"), effectiveSecurityRuntimeTest)

	cmd := exec.Command("go", "test", "-mod=mod", "-count=1", ".")
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test generated Fiber routes: %v\n%s", err, output)
	}
}

func writeEffectiveSecuritySpec(t *testing.T) (string, string) {
	t.Helper()

	workdir := t.TempDir()
	specPath := filepath.Join(workdir, "spec.yaml")
	mustWriteFile(t, specPath, `openapi: 3.0.3
info:
  title: effective security test
  version: 1.0.0
security:
  - BearerAuth:
      - x.OperationPermit(c, operationID)
    ApiKeyAuth: []
  - CookieAuth: []
paths:
  /inherited:
    get:
      operationId: inherited
      responses:
        "204":
          description: ok
  /public:
    get:
      operationId: public
      security: []
      responses:
        "204":
          description: ok
  /anonymous:
    get:
      operationId: anonymous
      security:
        - {}
      responses:
        "204":
          description: ok
  /optional:
    get:
      operationId: optional
      security:
        - {}
        - BearerAuth:
            - x.OperationPermit(c, operationID)
      responses:
        "204":
          description: ok
  /override:
    get:
      operationId: override
      security:
        - ApiKeyAuth:
            - x.OverridePermit(c)
      responses:
        "204":
          description: ok
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
    ApiKeyAuth:
      type: apiKey
      in: header
      name: X-API-Key
    CookieAuth:
      type: apiKey
      in: cookie
      name: session
x-check-rules:
  OperationPermit:
    useContext: true
    parameters:
      - name: operationID
        schema:
          type: string
  OverridePermit:
    useContext: true
`)
	return workdir, specPath
}

const effectiveSecurityRuntimeTest = `package securitytest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

type testHandler struct {
	calls map[string]int
}

func (h *testHandler) Inherited(c fiber.Ctx) error {
	h.calls["inherited"]++
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *testHandler) Public(c fiber.Ctx) error {
	h.calls["public"]++
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *testHandler) Anonymous(c fiber.Ctx) error {
	h.calls["anonymous"]++
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *testHandler) Optional(c fiber.Ctx) error {
	h.calls["optional"]++
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *testHandler) Override(c fiber.Ctx) error {
	h.calls["override"]++
	return c.SendStatus(fiber.StatusNoContent)
}

type testValidator struct {
	authErr       error
	authCalls     int
	preCalls      int
	postCalls     int
	operationIDs  []string
	overrideCalls int
	apiKeyScopes  []string
	bearerScopes  []string
	cookieScopes  []string
}

func (v *testValidator) AuthFunc(c fiber.Ctx) error {
	v.authCalls++
	v.apiKeyScopes, _ = fiber.ValueFromContext[[]string](c, ApiKeyAuthScopes)
	v.bearerScopes, _ = fiber.ValueFromContext[[]string](c, BearerAuthScopes)
	v.cookieScopes, _ = fiber.ValueFromContext[[]string](c, CookieAuthScopes)
	return v.authErr
}

func (v *testValidator) PreValidate(fiber.Ctx) error {
	v.preCalls++
	return nil
}

func (v *testValidator) PostValidate(fiber.Ctx) error {
	v.postCalls++
	return nil
}

func (v *testValidator) OperationPermit(_ fiber.Ctx, operationID string) error {
	v.operationIDs = append(v.operationIDs, operationID)
	return nil
}

func (v *testValidator) OverridePermit(fiber.Ctx) error {
	v.overrideCalls++
	return nil
}

func TestSecurityInheritanceRejectsUnauthenticatedRequests(t *testing.T) {
	handler := &testHandler{calls: map[string]int{}}
	validator := &testValidator{authErr: errors.New("missing credentials")}
	app := fiber.New()
	RegisterHandlers(app, NewXMiddleware(handler, validator))

	assertStatus(t, app, "/inherited", fiber.StatusUnauthorized)
	assertStatus(t, app, "/override", fiber.StatusUnauthorized)
	assertStatus(t, app, "/public", fiber.StatusNoContent)
	assertStatus(t, app, "/anonymous", fiber.StatusNoContent)
	assertStatus(t, app, "/optional", fiber.StatusNoContent)

	if validator.authCalls != 2 {
		t.Fatalf("AuthFunc calls = %d, want 2", validator.authCalls)
	}
	if handler.calls["inherited"] != 0 || handler.calls["override"] != 0 {
		t.Fatalf("protected handlers were called: %#v", handler.calls)
	}
	if handler.calls["public"] != 1 || handler.calls["anonymous"] != 1 || handler.calls["optional"] != 1 {
		t.Fatalf("anonymous handlers were not called exactly once: %#v", handler.calls)
	}
}

func TestInheritedSecurityPreservesSchemesAndChecks(t *testing.T) {
	handler := &testHandler{calls: map[string]int{}}
	validator := &testValidator{}
	app := fiber.New()
	RegisterHandlers(app, NewXMiddleware(handler, validator))

	assertStatus(t, app, "/inherited", fiber.StatusNoContent)
	if len(validator.apiKeyScopes) != 0 {
		t.Fatalf("inherited API key scopes = %#v, want empty", validator.apiKeyScopes)
	}
	if len(validator.bearerScopes) != 1 || validator.bearerScopes[0] != "x.OperationPermit(c, operationID)" {
		t.Fatalf("inherited bearer scopes = %#v", validator.bearerScopes)
	}
	if len(validator.cookieScopes) != 0 {
		t.Fatalf("inherited cookie scopes = %#v, want empty", validator.cookieScopes)
	}

	assertStatus(t, app, "/override", fiber.StatusNoContent)

	if validator.authCalls != 2 || validator.preCalls != 2 || validator.postCalls != 2 {
		t.Fatalf("validator calls = auth:%d pre:%d post:%d, want 2 each", validator.authCalls, validator.preCalls, validator.postCalls)
	}
	if len(validator.operationIDs) != 1 || validator.operationIDs[0] != "Inherited" {
		t.Fatalf("OperationPermit calls = %#v, want [Inherited]", validator.operationIDs)
	}
	if validator.overrideCalls != 1 {
		t.Fatalf("OverridePermit calls = %d, want 1", validator.overrideCalls)
	}
	if len(validator.apiKeyScopes) != 1 || validator.apiKeyScopes[0] != "x.OverridePermit(c)" {
		t.Fatalf("last API key scopes = %#v", validator.apiKeyScopes)
	}
}

func assertStatus(t *testing.T, app *fiber.App, path string, want int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("GET %s status = %d, want %d", path, resp.StatusCode, want)
	}
}
`

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
