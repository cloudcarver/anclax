package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestGenerateSupportsRelativeWorkdir(t *testing.T) {
	t.Chdir(t.TempDir())
	workdir := "nile-backend"
	mustWriteFile(t, filepath.Join(workdir, "go.mod"), "module example.com/test\n\ngo 1.24\n")
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "root.yaml"), `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths:
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
	mustWriteFile(t, filepath.Join(workdir, "api", "schemas", "counter.yaml"), `schemas:
  Counter:
    type: object
    required: [count]
    properties:
      count:
        type: integer
        format: int32
`)

	for _, tt := range []struct {
		name string
		path string
	}{
		{name: "directory", path: filepath.Join("api", "openapi")},
		{name: "file", path: filepath.Join("api", "openapi", "root.yaml")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			outPath := filepath.Join("pkg", "zgen", tt.name, "spec_gen.go")
			if err := Generate(workdir, Config{
				Path:    tt.path,
				Out:     outPath,
				Package: "apigen",
				Schemas: &schema_codegen.Config{Path: filepath.Join("api", "schemas"), Output: filepath.Join("pkg", "zgen", "schemas")},
			}); err != nil {
				t.Fatalf("generate: %v", err)
			}

			raw, err := os.ReadFile(filepath.Join(workdir, outPath))
			if err != nil {
				t.Fatalf("read output: %v", err)
			}
			for _, needle := range []string{
				`"example.com/test/pkg/zgen/schemas"`,
				"GetCounter(c fiber.Ctx) error",
			} {
				if !strings.Contains(string(raw), needle) {
					t.Fatalf("generated output missing %q", needle)
				}
			}
		})
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

	statusCall := "return xSecurityError(c, xCheckRuleStatusCode(err),"
	if got := strings.Count(out, statusCall); got != 3 {
		t.Fatalf("generated output contains %q %d times, want 3", statusCall, got)
	}
	if strings.Contains(out, "SendString(err.Error())") {
		t.Fatal("generated security middleware still returns raw errors")
	}
	if !strings.Contains(out, `return xSecurityError(c, fiber.StatusUnauthorized, "authentication")`) {
		t.Fatal("generated authentication middleware does not return a stable 401")
	}
	if !strings.Contains(out, "return c.Status(status).SendString(http.StatusText(status))") {
		t.Fatal("generated security middleware does not replace existing response bodies")
	}
}

func TestGeneratedSecurityMiddlewareRuntime(t *testing.T) {
	workdir := t.TempDir()
	if err := Generate(".", Config{
		Path:    filepath.Join("testdata", "x_check_rules_status.yaml"),
		Out:     filepath.Join(workdir, "spec_gen.go"),
		Package: "apigen",
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	moduleText := strings.Replace(string(module), "module github.com/cloudcarver/anclax", "module example.com/generated-security-test", 1)
	moduleText += "\nrequire github.com/cloudcarver/anclax v0.0.0\nreplace github.com/cloudcarver/anclax => " + strconv.Quote(root) + "\n"
	mustWriteFile(t, filepath.Join(workdir, "go.mod"), moduleText)
	for _, file := range []struct{ src, dst string }{
		{filepath.Join(root, "go.sum"), "go.sum"},
		{filepath.Join("testdata", "security_middleware_test.go.txt"), "security_middleware_test.go"},
	} {
		contents, err := os.ReadFile(file.src)
		if err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, filepath.Join(workdir, file.dst), string(contents))
	}
	cmd := exec.Command("go", "test", "-mod=mod", "-count=1", ".")
	cmd.Dir = workdir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated security middleware failed: %v\n%s", err, output)
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

func TestGenerateBoundsClientResponsesAndDefaultTimeout(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	specPath := filepath.Join(workdir, "spec.yaml")
	outPath := filepath.Join(workdir, "spec_gen.go")
	mustWriteFile(t, specPath, `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths:
  /health:
    get:
      operationId: GetHealth
      responses:
        '200':
          description: ok
`)

	if err := Generate(workdir, Config{
		Path:    specPath,
		Out:     outPath,
		Package: "apigen",
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	out := string(raw)
	for _, needle := range []string{
		"const DefaultHTTPClientTimeout = 30 * time.Second",
		"const MaxResponseBodyBytes int64 = 10 << 20",
		"var ErrResponseBodyTooLarge = errors.New(\"response body exceeds maximum size\")",
		"io.ReadAll(io.LimitReader(body, MaxResponseBodyBytes+1))",
		"client.Client = &http.Client{Timeout: DefaultHTTPClientTimeout}",
		"bodyBytes, err := readResponseBody(rsp.Body)",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("generated output missing %q", needle)
		}
	}
	if strings.Contains(out, "io.ReadAll(rsp.Body)") {
		t.Fatal("generated response parser still reads an unbounded body")
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

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	moduleText := strings.Replace(string(module), "module github.com/cloudcarver/anclax", "module example.com/securitytest", 1)
	moduleText += "\nrequire github.com/cloudcarver/anclax v0.0.0\nreplace github.com/cloudcarver/anclax => " + strconv.Quote(root) + "\n"
	mustWriteFile(t, filepath.Join(workdir, "go.mod"), moduleText)
	sums, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(workdir, "go.sum"), string(sums))
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
  /alternatives/{id}:
    get:
      operationId: alternatives
      parameters:
        - in: path
          name: id
          required: true
          schema:
            type: string
        - in: query
          name: mode
          schema:
            type: string
      security:
        - BearerAuth:
            - x.OperationPermit(c, operationID)
        - BearerAuth:
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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func (h *testHandler) Alternatives(c fiber.Ctx, id string, params AlternativesParams) error {
	if id != "item" || params.Mode == nil || *params.Mode != "test" {
		return fiber.ErrBadRequest
	}
	h.calls["alternatives"]++
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
	credentials   map[string]bool
	permitErr     error
	overrideErr   error
	writeBody     bool
	scopeHistory  [][]string
}

func (v *testValidator) AuthFunc(c fiber.Ctx) error {
	v.authCalls++
	var apiKey, bearer, cookie bool
	v.apiKeyScopes, apiKey = fiber.ValueFromContext[[]string](c, ApiKeyAuthScopes)
	v.bearerScopes, bearer = fiber.ValueFromContext[[]string](c, BearerAuthScopes)
	v.cookieScopes, cookie = fiber.ValueFromContext[[]string](c, CookieAuthScopes)
	v.scopeHistory = append(v.scopeHistory, append([]string(nil), v.bearerScopes...))
	if v.credentials != nil && ((apiKey && !v.credentials["apiKey"]) || (bearer && !v.credentials["bearer"]) || (cookie && !v.credentials["cookie"])) {
		if v.writeBody {
			c.Set("X-Internal-Debug", "missing-credential-canary")
			if err := c.SendString("failed-alternative-body-canary"); err != nil {
				return err
			}
		}
		return errors.New("missing-credential-canary")
	}
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
	return v.permitErr
}

func (v *testValidator) OverridePermit(fiber.Ctx) error {
	v.overrideCalls++
	return v.overrideErr
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

	if validator.authCalls != 3 {
		t.Fatalf("AuthFunc calls = %d, want 3", validator.authCalls)
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
	if validator.cookieScopes != nil {
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

func TestAlternativeAuthenticationAndCombinedSchemes(t *testing.T) {
	for _, passLocals := range []bool{false, true} {
	for _, tc := range []struct {
		name string
		credentials map[string]bool
		status int
		calls int
	}{
		{"cookie alternative", map[string]bool{"cookie": true}, fiber.StatusNoContent, 2},
		{"both first-alternative schemes", map[string]bool{"apiKey": true, "bearer": true}, fiber.StatusNoContent, 1},
		{"one required scheme missing", map[string]bool{"bearer": true}, fiber.StatusUnauthorized, 2},
		{"all credentials missing", map[string]bool{}, fiber.StatusUnauthorized, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &testHandler{calls: map[string]int{}}
			validator := &testValidator{credentials: tc.credentials, writeBody: true}
			app := fiber.New(fiber.Config{PassLocalsToContext: passLocals})
			app.Use(func(c fiber.Ctx) error {
				c.Set("X-Upstream", "preserved")
				return c.Next()
			})
			RegisterHandlers(app, NewXMiddleware(handler, validator))
			resp := assertStatus(t, app, "/inherited", tc.status)
			if resp.Header.Get("X-Upstream") != "preserved" {
				t.Fatal("security fallback lost upstream response headers")
			}
			if validator.authCalls != tc.calls {
				t.Fatalf("auth calls = %d, want %d", validator.authCalls, tc.calls)
			}
			wantHandlerCalls := 0
			if tc.status == fiber.StatusNoContent {
				wantHandlerCalls = 1
			}
			if handler.calls["inherited"] != wantHandlerCalls {
				t.Fatalf("handler calls = %d, want %d", handler.calls["inherited"], wantHandlerCalls)
			}
		})
	}
	}
}

func TestAlternativeScopesDoNotOverwriteEachOther(t *testing.T) {
	handler := &testHandler{calls: map[string]int{}}
	validator := &testValidator{permitErr: errors.New("permission-error-canary")}
	app := fiber.New()
	RegisterHandlers(app, NewXMiddleware(handler, validator))
	assertStatus(t, app, "/alternatives/item?mode=test", fiber.StatusNoContent)
	if validator.authCalls != 2 || len(validator.scopeHistory) != 2 {
		t.Fatalf("auth calls = %d, scopes = %#v", validator.authCalls, validator.scopeHistory)
	}
	if first, second := validator.scopeHistory[0], validator.scopeHistory[1]; len(first) != 1 || first[0] != "x.OperationPermit(c, operationID)" || len(second) != 1 || second[0] != "x.OverridePermit(c)" {
		t.Fatalf("alternative scopes = %#v", validator.scopeHistory)
	}
	if len(validator.operationIDs) != 1 || validator.overrideCalls != 1 || handler.calls["alternatives"] != 1 {
		t.Fatalf("permit calls = %#v / %d, handlers = %#v", validator.operationIDs, validator.overrideCalls, handler.calls)
	}
}

func TestAllAlternativeChecksFailWithoutCallingHandler(t *testing.T) {
	handler := &testHandler{calls: map[string]int{}}
	validator := &testValidator{
		permitErr: errors.New("first-permission-error-canary"),
		overrideErr: fiber.NewError(fiber.StatusTooManyRequests, "second-permission-error-canary"),
	}
	app := fiber.New()
	RegisterHandlers(app, NewXMiddleware(handler, validator))
	assertStatus(t, app, "/alternatives/item?mode=test", fiber.StatusTooManyRequests)
	if handler.calls["alternatives"] != 0 || validator.authCalls != 2 || validator.preCalls != 2 || validator.postCalls != 0 {
		t.Fatalf("handler calls = %#v, validation calls = auth:%d pre:%d post:%d", handler.calls, validator.authCalls, validator.preCalls, validator.postCalls)
	}
}

func assertStatus(t *testing.T, app *fiber.App, path string, want int) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}
	if strings.Contains(string(body), "canary") {
		t.Fatalf("security failure disclosed private details: %s", body)
	}
	if resp.Header.Get("X-Internal-Debug") != "" {
		t.Fatal("failed security alternative leaked response headers")
	}
	if resp.StatusCode != want {
		t.Fatalf("GET %s status = %d, want %d", path, resp.StatusCode, want)
	}
	return resp
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
