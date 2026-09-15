package dst

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedScriptFailuresReachCaller(t *testing.T) {
	spec, err := ParseHybridSpec([]byte(`
version: dst/hybrid/v1alpha1
package: scriptprobe
interfaces:
  Worker:
    methods:
      - Claim(ctx context.Context) error
instances:
  worker: Worker
scenarios:
  - name: fatal
    steps:
      - id: check
        script: |
          _ = ctx
          require.Equal(t, 1, 2, "fatal assertion")
  - name: nonfatal
    steps:
      - id: check
        script: |
          _ = ctx
          t.Errorf("nonfatal assertion")
  - name: panic
    steps:
      - id: check
        script: |
          _ = ctx
          panic("unexpected panic")
  - name: success
    steps:
      - id: check
        script: |
          _ = ctx
          require.Equal(t, 1, 1)
`))
	require.NoError(t, err)
	code, err := GenerateHybridGo(spec, "")
	require.NoError(t, err)
	dir := t.TempDir()
	generated := filepath.Join(dir, "probe.go")
	testFile := filepath.Join(dir, "probe_test.go")
	require.NoError(t, os.WriteFile(generated, []byte(code), 0600))
	require.NoError(t, os.WriteFile(testFile, []byte(`package scriptprobe
import (
 "context"
 "strings"
 "testing"
)
func TestScriptResults(t *testing.T) {
 for _, tc := range []struct {
  name string
  run func(context.Context, Actors) error
  want string
 }{
  {"fatal", RunScenarioFatal, "fatal assertion"},
  {"nonfatal", RunScenarioNonfatal, "nonfatal assertion"},
  {"panic", RunScenarioPanic, "unexpected panic"},
  {"success", RunScenarioSuccess, ""},
 } {
  err := tc.run(context.Background(), Actors{})
  if tc.want == "" {
   if err != nil { t.Errorf("%s: %v", tc.name, err) }
  } else if err == nil || !strings.Contains(err.Error(), tc.want) {
   t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
  }
 }
}
`), 0600))
	out, err := exec.Command("go", "test", generated, testFile).CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func TestValidateHybridSpecAndGenerate(t *testing.T) {
	raw := []byte(`
version: dst/hybrid/v1alpha1
package: dstgen
interfaces:
  Worker:
    methods:
      - Claim(ctx context.Context) error
      - Complete(ctx context.Context, taskID int32) error
  TaskStore:
    methods:
      - Enqueue(ctx context.Context, task string) error
      - GetStatus(ctx context.Context, task string) (string, error)
instances:
  worker1: Worker
  worker2: Worker
  store: TaskStore
scenarios:
  - name: strict_flow
    steps:
      - id: s1
        parallel:
          store:
            - Enqueue(ctx, "S1")
      - id: s2
        parallel:
          worker1:
            - Claim(ctx)
          worker2:
            - Claim(ctx)
      - id: s3
        script: |
          _ = ctx
`)

	spec, err := ParseHybridSpec(raw)
	require.NoError(t, err)
	require.NoError(t, ValidateHybridSpec(spec))

	code, err := GenerateHybridGo(spec, "")
	require.NoError(t, err)
	require.Contains(t, code, "type Worker interface")
	require.Contains(t, code, "type TaskStore interface")
	require.Contains(t, code, "func RunScenario")
	require.Contains(t, code, "func RunAll")
	require.Contains(t, code, "RunAllWithReport")
}

func TestValidateHybridSpecRejectUnknownMethod(t *testing.T) {
	raw := []byte(`
version: dst/hybrid/v1alpha1
interfaces:
  Worker:
    methods:
      - Claim(ctx context.Context) error
instances:
  worker1: Worker
scenarios:
  - name: bad
    steps:
      - id: s1
        parallel:
          worker1:
            - NotExists(ctx)
`)

	spec, err := ParseHybridSpec(raw)
	require.NoError(t, err)
	err = ValidateHybridSpec(spec)
	require.Error(t, err)
	require.ErrorContains(t, err, "unknown method")
}

func TestParseMethodSignature(t *testing.T) {
	m, err := ParseMethodSignature("Claim(ctx context.Context) error")
	require.NoError(t, err)
	require.Equal(t, "Claim", m.Name)
	require.Len(t, m.Params, 1)
	require.Equal(t, "ctx", m.Params[0].Name)
	require.Equal(t, "context.Context", m.Params[0].Type)
	require.Equal(t, "error", m.Returns)

	withReturn, err := ParseMethodSignature("GetStatus(ctx context.Context) (string, error)")
	require.NoError(t, err)
	require.Equal(t, "GetStatus", withReturn.Name)
	require.Equal(t, "(string, error)", withReturn.Returns)
}

func TestParseCallExpression(t *testing.T) {
	c, err := ParseCallExpression("Enqueue(ctx, map[string]int{\"a\":1})")
	require.NoError(t, err)
	require.Equal(t, "Enqueue", c.Method)
	require.Len(t, c.Args, 2)
	require.True(t, strings.Contains(c.Args[1], "map[string]int"))
}
