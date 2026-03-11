package chaos

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Report struct {
	mu          sync.Mutex
	RunID       string         `json:"runID"`
	Seed        int64          `json:"seed,omitempty"`
	StartedAt   time.Time      `json:"startedAt"`
	FinishedAt  time.Time      `json:"finishedAt,omitempty"`
	ArtifactDir string         `json:"artifactDir"`
	Events      []ReportEvent  `json:"events"`
	Summary     *ReportSummary `json:"summary,omitempty"`
	Failure     *FailureReport `json:"failure,omitempty"`
}

type ReportSummary struct {
	Components ComponentSummary `json:"components"`
	Tasks      TaskSummary      `json:"tasks"`
	Scenario   ScenarioSummary  `json:"scenario"`
}

type ComponentSummary struct {
	DownCounts    map[string]int `json:"downCounts,omitempty"`
	RestartCounts map[string]int `json:"restartCounts,omitempty"`
}

type TaskSummary struct {
	Submitted int   `json:"submitted"`
	Expected  int   `json:"expected"`
	Observed  int64 `json:"observed"`
	Processed int64 `json:"processed"`
	Completed int64 `json:"completed"`
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Failed    int64 `json:"failed"`
	Cancelled int64 `json:"cancelled"`
	Paused    int64 `json:"paused"`
	Retried   int64 `json:"retried"`
}

type ScenarioSummary struct {
	WorkerDisruptions    int `json:"workerDisruptions"`
	PostgresRestarts     int `json:"postgresRestarts"`
	ControlPlaneOutages  int `json:"controlPlaneOutages"`
	RuntimeConfigUpdates int `json:"runtimeConfigUpdates"`
	ReplacementWorkers   int `json:"replacementWorkers"`
	ActiveWorkers        int `json:"activeWorkers"`
	RetiredWorkers       int `json:"retiredWorkers"`
}

type ReportEvent struct {
	At      time.Time      `json:"at"`
	Kind    string         `json:"kind"`
	Target  string         `json:"target,omitempty"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

type FailureReport struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func NewReport(runID string, seed int64, artifactDir string) *Report {
	return &Report{
		RunID:       runID,
		Seed:        seed,
		ArtifactDir: artifactDir,
		StartedAt:   time.Now().UTC(),
		Events:      make([]ReportEvent, 0, 32),
	}
}

func (r *Report) AddEvent(kind string, target string, message string, fields map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = append(r.Events, ReportEvent{
		At:      time.Now().UTC(),
		Kind:    kind,
		Target:  target,
		Message: message,
		Fields:  cloneFields(fields),
	})
}

func (r *Report) MarkFailure(message string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Failure = &FailureReport{At: time.Now().UTC(), Message: message}
}

func (r *Report) SetSummary(summary *ReportSummary) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Summary = summary
}

func (r *Report) Write() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.FinishedAt = time.Now().UTC()
	if err := os.MkdirAll(r.ArtifactDir, 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.ArtifactDir, "report.json"), content, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.ArtifactDir, "summary.txt"), []byte(renderHumanSummary(r)), 0o644)
}

func cloneFields(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func renderHumanSummary(r *Report) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "taskcore chaos run\n")
	fmt.Fprintf(&b, "run_id: %s\n", r.RunID)
	fmt.Fprintf(&b, "seed: %d\n", r.Seed)
	fmt.Fprintf(&b, "started_at: %s\n", r.StartedAt.Format(time.RFC3339))
	if !r.FinishedAt.IsZero() {
		fmt.Fprintf(&b, "finished_at: %s\n", r.FinishedAt.Format(time.RFC3339))
		fmt.Fprintf(&b, "duration: %s\n", r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond))
	}
	fmt.Fprintf(&b, "artifact_dir: %s\n", r.ArtifactDir)
	if r.Failure != nil {
		fmt.Fprintf(&b, "failure: %s\n", r.Failure.Message)
	} else {
		fmt.Fprintf(&b, "failure: <none>\n")
	}
	if r.Summary == nil {
		fmt.Fprintf(&b, "summary: <none>\n")
		return b.String()
	}

	fmt.Fprintf(&b, "\nscenario\n")
	fmt.Fprintf(&b, "  worker_disruptions: %d\n", r.Summary.Scenario.WorkerDisruptions)
	fmt.Fprintf(&b, "  postgres_restarts: %d\n", r.Summary.Scenario.PostgresRestarts)
	fmt.Fprintf(&b, "  control_plane_outages: %d\n", r.Summary.Scenario.ControlPlaneOutages)
	fmt.Fprintf(&b, "  runtime_config_updates: %d\n", r.Summary.Scenario.RuntimeConfigUpdates)
	fmt.Fprintf(&b, "  replacement_workers: %d\n", r.Summary.Scenario.ReplacementWorkers)
	fmt.Fprintf(&b, "  active_workers: %d\n", r.Summary.Scenario.ActiveWorkers)
	fmt.Fprintf(&b, "  retired_workers: %d\n", r.Summary.Scenario.RetiredWorkers)

	fmt.Fprintf(&b, "\ntasks\n")
	fmt.Fprintf(&b, "  submitted: %d\n", r.Summary.Tasks.Submitted)
	fmt.Fprintf(&b, "  expected: %d\n", r.Summary.Tasks.Expected)
	fmt.Fprintf(&b, "  observed: %d\n", r.Summary.Tasks.Observed)
	fmt.Fprintf(&b, "  processed: %d\n", r.Summary.Tasks.Processed)
	fmt.Fprintf(&b, "  completed: %d\n", r.Summary.Tasks.Completed)
	fmt.Fprintf(&b, "  pending: %d\n", r.Summary.Tasks.Pending)
	fmt.Fprintf(&b, "  running: %d\n", r.Summary.Tasks.Running)
	fmt.Fprintf(&b, "  failed: %d\n", r.Summary.Tasks.Failed)
	fmt.Fprintf(&b, "  cancelled: %d\n", r.Summary.Tasks.Cancelled)
	fmt.Fprintf(&b, "  paused: %d\n", r.Summary.Tasks.Paused)
	fmt.Fprintf(&b, "  retried: %d\n", r.Summary.Tasks.Retried)

	fmt.Fprintf(&b, "\ncomponent_down_counts\n")
	writeSortedIntMap(&b, r.Summary.Components.DownCounts)
	fmt.Fprintf(&b, "\ncomponent_restart_counts\n")
	writeSortedIntMap(&b, r.Summary.Components.RestartCounts)
	return b.String()
}

func writeSortedIntMap(b *strings.Builder, values map[string]int) {
	if len(values) == 0 {
		fmt.Fprintf(b, "  <none>\n")
		return
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "  %s: %d\n", k, values[k])
	}
}
