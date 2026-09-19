package bundle

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPrepareDirectoryMergesSpecsAndRewritesRefs(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
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
      operationId: getCounter
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: ../schemas/counter.yaml#schemas/Counter
x-functions:
  GetOrgID:
    useContext: true
    return:
      schema:
        type: integer
        format: int32
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

	source, err := Prepare(workdir, filepath.Join("api", "openapi"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got, want := filepath.ToSlash(source.VirtualPath), filepath.ToSlash(filepath.Join(workdir, "api", "openapi", VirtualBundleName)); got != want {
		t.Fatalf("virtual path = %q, want %q", got, want)
	}
	if !strings.Contains(string(source.Bytes), "$ref: ../schemas/counter.yaml#/schemas/Counter") {
		t.Fatalf("merged yaml missing rewritten schema ref: %s", source.Bytes)
	}
	if !strings.Contains(string(source.Bytes), "x-functions:") {
		t.Fatalf("merged yaml missing x-functions: %s", source.Bytes)
	}

	doc, virtualPath, err := Load(workdir, filepath.Join("api", "openapi"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if virtualPath != source.VirtualPath {
		t.Fatalf("virtual path = %q, want %q", virtualPath, source.VirtualPath)
	}
	if doc.Paths == nil || doc.Paths.Value("/counter") == nil || doc.Paths.Value("/counter").Get == nil {
		t.Fatalf("loaded doc missing merged GET /counter operation")
	}
	if doc.Extensions == nil || doc.Extensions["x-functions"] == nil {
		t.Fatalf("loaded doc missing x-functions extension")
	}
}

func TestPrepareDirectoryRejectsDuplicateOperation(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "a.yaml"), `paths:
  /health:
    get:
      operationId: getHealthA
      responses:
        '200':
          description: ok
`)
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "b.yaml"), `paths:
  /health:
    get:
      operationId: getHealthB
      responses:
        '200':
          description: ok
`)

	_, err := Prepare(workdir, filepath.Join("api", "openapi"))
	if err == nil {
		t.Fatal("expected duplicate operation error")
	}
	if !strings.Contains(err.Error(), "duplicate operation GET /health") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareDirectoryRejectsDuplicateComponentAndExtensionItems(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "a.yaml"), `components:
  schemas:
    User:
      type: object
x-functions:
  GetOrgID:
    return:
      schema:
        type: integer
        format: int32
`)
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "b.yaml"), `components:
  schemas:
    User:
      type: object
x-functions:
  GetOrgID:
    return:
      schema:
        type: string
`)

	_, err := Prepare(workdir, filepath.Join("api", "openapi"))
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	if !strings.Contains(err.Error(), "duplicate component schemas.User") && !strings.Contains(err.Error(), "duplicate x-functions.GetOrgID") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareDirectoryAllowsDifferentMethodsOnSamePath(t *testing.T) {
	t.Parallel()

	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "root.yaml"), `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths:
  /health:
    get:
      operationId: getHealth
      responses:
        '200':
          description: ok
`)
	mustWriteFile(t, filepath.Join(workdir, "api", "openapi", "post.yaml"), `paths:
  /health:
    post:
      operationId: postHealth
      responses:
        '202':
          description: ok
`)

	source, err := Prepare(workdir, filepath.Join("api", "openapi"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	out := string(source.Bytes)
	if !strings.Contains(out, "get:") || !strings.Contains(out, "post:") {
		t.Fatalf("merged yaml missing methods: %s", out)
	}
}

func TestLoadRejectsRemoteReferencesWithoutContactingServer(t *testing.T) {
	t.Parallel()

	var contacted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted.Store(true)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	workdir := t.TempDir()
	mustWriteFile(t, filepath.Join(workdir, "openapi.yaml"), `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths: {}
components:
  schemas:
    Remote:
      $ref: `+server.URL+`/schema.yaml#/Remote
`)

	_, _, err := Load(workdir, "openapi.yaml")
	if err == nil || !strings.Contains(err.Error(), "remote OpenAPI references are disabled") {
		t.Fatalf("Load error = %v, want remote-reference rejection", err)
	}
	if contacted.Load() {
		t.Fatal("remote OpenAPI server was contacted")
	}
}

func TestLoadRejectsLocalReferenceOutsideWorkdir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workdir := filepath.Join(root, "project")
	mustWriteFile(t, filepath.Join(root, "outside.yaml"), `Outside:
  type: string
`)
	mustWriteFile(t, filepath.Join(workdir, "openapi.yaml"), `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths: {}
components:
  schemas:
    Outside:
      $ref: ../outside.yaml#/Outside
`)

	_, _, err := Load(workdir, "openapi.yaml")
	if err == nil || !strings.Contains(err.Error(), "outside workdir") {
		t.Fatalf("Load error = %v, want workdir-boundary rejection", err)
	}
}

func TestLoadRejectsSymlinkReferenceOutsideWorkdir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows")
	}
	t.Parallel()

	root := t.TempDir()
	workdir := filepath.Join(root, "project")
	mustWriteFile(t, filepath.Join(root, "outside.yaml"), `Outside:
  type: string
`)
	mustWriteFile(t, filepath.Join(workdir, "openapi.yaml"), `openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths: {}
components:
  schemas:
    Outside:
      $ref: linked.yaml#/Outside
`)
	if err := os.Symlink(filepath.Join(root, "outside.yaml"), filepath.Join(workdir, "linked.yaml")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, _, err := Load(workdir, "openapi.yaml")
	if err == nil || !strings.Contains(err.Error(), "outside workdir") {
		t.Fatalf("Load error = %v, want symlink boundary rejection", err)
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
