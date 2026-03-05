package dst

import (
	"bytes"
	"sort"
	"strings"
	"text/template"

	"github.com/pkg/errors"
)

type hybridTemplateData struct {
	Package    string
	Interfaces []hybridInterfaceData
	Instances  []hybridInstanceData
	Scenarios  []hybridScenarioData
	HasScript  bool
}

type hybridInterfaceData struct {
	Name    string
	Methods []ParsedMethod
}

type hybridInstanceData struct {
	InstanceName string
	FieldName    string
	Interface    string
}

type hybridScenarioData struct {
	Name     string
	FuncName string
	Steps    []hybridStepData
}

type hybridStepData struct {
	ID        string
	FuncName  string
	ActorOps  []hybridActorOpsData
	Script    string
	HasScript bool
}

type hybridActorOpsData struct {
	ActorInstance string
	ActorField    string
	Calls         []hybridCallData
}

type hybridCallData struct {
	Raw        string
	Method     string
	ArgsJoined string
}

func GenerateHybridGo(spec *HybridSpec, pkg string) (string, error) {
	if err := ValidateHybridSpec(spec); err != nil {
		return "", err
	}
	if pkg == "" {
		pkg = spec.Package
	}
	if pkg == "" {
		pkg = "dstgen"
	}

	data, err := buildHybridTemplateData(spec, pkg)
	if err != nil {
		return "", err
	}

	tpl := template.Must(template.New("hybrid").Parse(hybridTemplate))
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", errors.Wrap(err, "execute hybrid code template")
	}
	return buf.String(), nil
}

func buildHybridTemplateData(spec *HybridSpec, pkg string) (*hybridTemplateData, error) {
	ifaceNames := sortedMapKeys(spec.Interfaces)
	interfaces := make([]hybridInterfaceData, 0, len(ifaceNames))
	parsedMethodsByInterface := map[string]map[string]ParsedMethod{}
	for _, ifaceName := range ifaceNames {
		ifaceSpec := spec.Interfaces[ifaceName]
		methods := make([]ParsedMethod, 0, len(ifaceSpec.Methods))
		methodSet := map[string]ParsedMethod{}
		for _, raw := range ifaceSpec.Methods {
			m, err := ParseMethodSignature(raw)
			if err != nil {
				return nil, errors.Wrapf(err, "interface %s", ifaceName)
			}
			methods = append(methods, m)
			methodSet[m.Name] = m
		}
		parsedMethodsByInterface[ifaceName] = methodSet
		interfaces = append(interfaces, hybridInterfaceData{Name: ifaceName, Methods: methods})
	}

	instanceNames := sortedMapKeys(spec.Instances)
	instances := make([]hybridInstanceData, 0, len(instanceNames))
	for _, instance := range instanceNames {
		instances = append(instances, hybridInstanceData{
			InstanceName: instance,
			FieldName:    toExportedIdentifier(instance),
			Interface:    spec.Instances[instance],
		})
	}

	scenarios := make([]hybridScenarioData, 0, len(spec.Scenarios))
	hasScript := false
	for _, scenario := range spec.Scenarios {
		sc := hybridScenarioData{Name: scenario.Name, FuncName: "RunScenario" + toExportedIdentifier(scenario.Name)}
		for _, st := range scenario.Steps {
			stepData := hybridStepData{
				ID:       st.ID,
				FuncName: "runStep" + toExportedIdentifier(scenario.Name) + toExportedIdentifier(st.ID),
			}
			if strings.TrimSpace(st.Script) != "" {
				stepData.Script = indentScript(st.Script)
				stepData.HasScript = true
				hasScript = true
				sc.Steps = append(sc.Steps, stepData)
				continue
			}
			actors := sortedMapKeys(st.Parallel)
			for _, actor := range actors {
				iface := spec.Instances[actor]
				methodSet := parsedMethodsByInterface[iface]
				actorData := hybridActorOpsData{
					ActorInstance: actor,
					ActorField:    toExportedIdentifier(actor),
				}
				for _, rawCall := range st.Parallel[actor] {
					call, err := ParseCallExpression(rawCall)
					if err != nil {
						return nil, errors.Wrapf(err, "scenario %s step %s actor %s", scenario.Name, st.ID, actor)
					}
					if _, ok := methodSet[call.Method]; !ok {
						return nil, errors.Errorf("scenario %s step %s actor %s calls unknown method %s", scenario.Name, st.ID, actor, call.Method)
					}
					actorData.Calls = append(actorData.Calls, hybridCallData{
						Raw:        rawCall,
						Method:     call.Method,
						ArgsJoined: strings.Join(call.Args, ", "),
					})
				}
				stepData.ActorOps = append(stepData.ActorOps, actorData)
			}
			sc.Steps = append(sc.Steps, stepData)
		}
		scenarios = append(scenarios, sc)
	}

	return &hybridTemplateData{
		Package:    pkg,
		Interfaces: interfaces,
		Instances:  instances,
		Scenarios:  scenarios,
		HasScript:  hasScript,
	}, nil
}

func sortedMapKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func toExportedIdentifier(raw string) string {
	if raw == "" {
		return "X"
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	})
	if len(parts) == 0 {
		return "X"
	}
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	out := strings.Join(parts, "")
	if out == "" {
		out = "X"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "X" + out
	}
	return out
}

func indentScript(raw string) string {
	trimmed := strings.TrimRight(raw, "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = "\t"
			continue
		}
		lines[i] = "\t" + line
	}
	return strings.Join(lines, "\n")
}

const hybridTemplate = `// Code generated by dst gen; DO NOT EDIT.

package {{.Package}}

import (
	"context"
	"fmt"
	"sync"
	"time"
{{- if .HasScript}}

	"github.com/stretchr/testify/require"
{{- end}}
)

{{range .Interfaces}}
type {{.Name}} interface {
{{- range .Methods}}
	{{.Name}}({{- range $i, $p := .Params}}{{if $i}}, {{end}}{{$p.Name}} {{$p.Type}}{{- end}}) {{.Returns}}
{{- end}}
}

{{end}}
type Actors struct {
{{- range .Instances}}
	{{.FieldName}} {{.Interface}}
{{- end}}
}

type InitActorsFunc func(ctx context.Context) (Actors, error)

func Init(ctx context.Context, initActors InitActorsFunc) (Actors, error) {
	if initActors == nil {
		return Actors{}, fmt.Errorf("initActors is nil")
	}
	actors, err := initActors(ctx)
	if err != nil {
		return Actors{}, err
	}
	if err := ValidateActors(actors); err != nil {
		return Actors{}, err
	}
	return actors, nil
}

func ValidateActors(actors Actors) error {
{{- range .Instances}}
	if actors.{{.FieldName}} == nil {
		return fmt.Errorf("actors.{{.FieldName}} (instance {{.InstanceName}}) is nil")
	}
{{- end}}
	return nil
}

type varStore struct {
	mu     sync.RWMutex
	values map[string]any
}

func newVarStore() *varStore {
	return &varStore{values: map[string]any{}}
}

func (v *varStore) Set(name string, value any) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[name] = value
}

func (v *varStore) Get(name string) (any, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	val, ok := v.values[name]
	return val, ok
}

{{- if .HasScript}}
var _ = require.NoError

type scriptFail struct {
	err error
}

type scriptT struct {
	err    error
	failed bool
}

func (t *scriptT) Helper() {}

func (t *scriptT) Errorf(format string, args ...interface{}) {
	t.err = fmt.Errorf(format, args...)
	t.failed = true
}

func (t *scriptT) FailNow() {
	t.failed = true
	panic(scriptFail{err: t.err})
}
{{- end}}

type RunOptions struct {
	Repeat          int
	ContinueOnError bool
}

type RunResult struct {
	Iteration int
	Duration  time.Duration
	Err       error
}

type RunReport struct {
	TotalRuns  int
	FailedRuns int
	Runs       []RunResult
}

func RunAll(ctx context.Context, initActors InitActorsFunc) error {
	actors, err := Init(ctx, initActors)
	if err != nil {
		return err
	}
	return runAllWithActors(ctx, actors)
}

func RunAllWithReport(ctx context.Context, initActors InitActorsFunc, opts RunOptions) (RunReport, error) {
	if opts.Repeat <= 0 {
		opts.Repeat = 1
	}
	report := RunReport{TotalRuns: opts.Repeat}
	for i := 0; i < opts.Repeat; i++ {
		startedAt := time.Now()
		actors, err := Init(ctx, initActors)
		if err == nil {
			err = runAllWithActors(ctx, actors)
		}
		report.Runs = append(report.Runs, RunResult{
			Iteration: i + 1,
			Duration:  time.Since(startedAt),
			Err:       err,
		})
		if err != nil {
			report.FailedRuns++
			if !opts.ContinueOnError {
				return report, err
			}
		}
	}
	if report.FailedRuns > 0 {
		return report, fmt.Errorf("dst: %d/%d run(s) failed", report.FailedRuns, report.TotalRuns)
	}
	return report, nil
}

func runAllWithActors(ctx context.Context, actors Actors) error {
{{- range .Scenarios}}
	if err := {{.FuncName}}(ctx, actors); err != nil {
		return fmt.Errorf("scenario {{.Name}}: %w", err)
	}
{{- end}}
	return nil
}

{{range .Scenarios}}
func {{.FuncName}}(ctx context.Context, actors Actors) error {
	vars := newVarStore()
{{- range .Steps}}
	if err := {{.FuncName}}(ctx, actors, vars); err != nil {
		return fmt.Errorf("step {{.ID}}: %w", err)
	}
{{- end}}
	return nil
}

{{range .Steps}}
func {{.FuncName}}(parent context.Context, actors Actors, vars *varStore) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
{{- if .HasScript}}
	var err error
	t := &scriptT{}
	set := func(name string, value any) {
		vars.Set(name, value)
	}
	get := func(name string) any {
		val, _ := vars.Get(name)
		return val
	}
	_ = t
	_ = set
	_ = get
	defer func() {
		if r := recover(); r != nil {
			if fail, ok := r.(scriptFail); ok {
				if fail.err != nil {
					err = fail.err
					return
				}
				err = fmt.Errorf("script failed")
				return
			}
			err = fmt.Errorf("script panic: %v", r)
			return
		}
		if t.failed && err == nil {
			if t.err != nil {
				err = t.err
			} else {
				err = fmt.Errorf("script failed")
			}
		}
	}()
{{.Script}}
	return err
{{- else}}
	var wg sync.WaitGroup
	errCh := make(chan error, {{len .ActorOps}})
{{- range .ActorOps}}
	{{$actor := .}}
	wg.Add(1)
	go func() {
		defer wg.Done()
{{- range .Calls}}
		if err := actors.{{$actor.ActorField}}.{{.Method}}({{.ArgsJoined}}); err != nil {
			errCh <- fmt.Errorf("actor {{$actor.ActorInstance}} call %s: %w", {{printf "%q" .Raw}}, err)
			cancel()
			return
		}
{{- end}}
	}()
{{- end}}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
{{- end}}
}

{{end}}
{{end}}
`
