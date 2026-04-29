package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/taskcore/ctrl"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

type stressProbeRequest struct {
	TaskName         string   `json:"taskName"`
	JobID            int64    `json:"jobID"`
	SleepMs          int32    `json:"sleepMs"`
	Group            string   `json:"group"`
	FailMode         string   `json:"failMode,omitempty"`
	Labels           []string `json:"labels,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	DelayMs          int32    `json:"delayMs,omitempty"`
	UniqueTag        string   `json:"uniqueTag,omitempty"`
	RetryInterval    string   `json:"retryInterval,omitempty"`
	RetryMaxAttempts *int32   `json:"retryMaxAttempts,omitempty"`
	SignalBaseURL    string   `json:"signalBaseURL,omitempty"`
	SignalIntervalMs int32    `json:"signalIntervalMs,omitempty"`
}

type cancelObservableProbeRequest struct {
	TaskName         string   `json:"taskName"`
	Group            string   `json:"group"`
	Labels           []string `json:"labels,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	UniqueTag        string   `json:"uniqueTag,omitempty"`
	SignalBaseURL    string   `json:"signalBaseURL,omitempty"`
	SignalIntervalMs int32    `json:"signalIntervalMs,omitempty"`
}

type taskControlRequest struct {
	UniqueTag string `json:"uniqueTag"`
}

type taskTagsControlRequest struct {
	Tags          []string   `json:"tags"`
	ExceptTagSets [][]string `json:"exceptTagSets,omitempty"`
}

type runtimeConfigRequest struct {
	MaxStrictPercentage int32            `json:"maxStrictPercentage"`
	DefaultWeight       int32            `json:"defaultWeight"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type runtimeConfigResponse struct {
	TaskID int32 `json:"taskID"`
}

const chaosControlRequestTimeout = 5 * time.Minute

type signalSnapshot struct {
	TaskID        int32      `json:"taskID"`
	Count         int64      `json:"count"`
	LastEmittedAt *time.Time `json:"lastEmittedAt,omitempty"`
}

type signalState struct {
	Count         int64
	LastEmittedAt time.Time
}

type app struct {
	store        taskcore.TaskStoreInterface
	runner       taskgen.TaskRunner
	controlPlane *ctrl.WorkerControlPlane
	model        model.ModelInterface
	signalMu     sync.Mutex
	signals      map[int32]signalState
}

func main() {
	dsn := os.Getenv("CHAOS_DSN")
	if dsn == "" {
		log.Fatal("CHAOS_DSN is required")
	}
	addr := os.Getenv("CHAOS_HTTP_ADDR")
	if addr == "" {
		addr = ":18080"
	}

	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}}
	libCfg := config.DefaultLibConfig()
	cm := closer.NewCloserManager()
	defer cm.Close()

	m, err := model.NewModel(cfg, libCfg, cm)
	if err != nil {
		log.Fatal(err)
	}
	store := taskcore.NewTaskStore(m)
	runner := taskgen.NewTaskRunner(store)
	controlPlane := ctrl.NewWorkerControlPlane(m, runner, store)

	a := &app{store: store, runner: runner, controlPlane: controlPlane, model: m, signals: map[int32]signalState{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/signals/emit", a.handleSignalEmit)
	mux.HandleFunc("/signals/", a.handleSignal)
	mux.HandleFunc("/tasks/stress-probe", a.handleStressProbe)
	mux.HandleFunc("/tasks/cancel-observable-probe", a.handleCancelObservableProbe)
	mux.HandleFunc("/tasks/pause", a.handlePauseTask)
	mux.HandleFunc("/tasks/cancel", a.handleCancelTask)
	mux.HandleFunc("/tasks/resume", a.handleResumeTask)
	mux.HandleFunc("/tasks/pause-by-tags", a.handlePauseTasksByTags)
	mux.HandleFunc("/tasks/cancel-by-tags", a.handleCancelTasksByTags)
	mux.HandleFunc("/tasks/resume-by-tags", a.handleResumeTasksByTags)
	mux.HandleFunc("/runtime-config", a.handleRuntimeConfig)

	server := &http.Server{Addr: addr, Handler: mux}
	log.Printf("control plane listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (a *app) handleSignalEmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TaskID int32 `json:"taskID"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TaskID <= 0 {
		http.Error(w, "taskID must be positive", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, a.emitSignal(req.TaskID))
}

func (a *app) handleSignal(w http.ResponseWriter, r *http.Request) {
	taskID, err := parseSignalTaskID(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.signalSnapshot(taskID))
	case http.MethodDelete:
		a.deleteSignal(taskID)
		writeJSON(w, http.StatusOK, signalSnapshot{TaskID: taskID})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) emitSignal(taskID int32) signalSnapshot {
	a.signalMu.Lock()
	defer a.signalMu.Unlock()
	state := a.signals[taskID]
	state.Count++
	state.LastEmittedAt = time.Now().UTC()
	a.signals[taskID] = state
	t := state.LastEmittedAt
	return signalSnapshot{TaskID: taskID, Count: state.Count, LastEmittedAt: &t}
}

func (a *app) signalSnapshot(taskID int32) signalSnapshot {
	a.signalMu.Lock()
	defer a.signalMu.Unlock()
	state, ok := a.signals[taskID]
	if !ok {
		return signalSnapshot{TaskID: taskID}
	}
	out := signalSnapshot{TaskID: taskID, Count: state.Count}
	if !state.LastEmittedAt.IsZero() {
		t := state.LastEmittedAt.UTC()
		out.LastEmittedAt = &t
	}
	return out
}

func (a *app) deleteSignal(taskID int32) {
	a.signalMu.Lock()
	defer a.signalMu.Unlock()
	delete(a.signals, taskID)
}

func parseSignalTaskID(path string) (int32, error) {
	raw := strings.TrimPrefix(path, "/signals/")
	if raw == "" || raw == path {
		return 0, fmt.Errorf("taskID is required")
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid taskID %q", raw)
	}
	return int32(v), nil
}

func (a *app) handleStressProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req stressProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TaskName == "" {
		http.Error(w, "taskName is required", http.StatusBadRequest)
		return
	}
	uniqueTag := req.UniqueTag
	if uniqueTag == "" {
		uniqueTag = req.TaskName
	}
	overrides := []taskcore.TaskOverride{taskcore.WithUniqueTag(uniqueTag)}
	if len(req.Labels) > 0 {
		overrides = append(overrides, taskcore.WithLabels(req.Labels))
	}
	if len(req.Tags) > 0 {
		overrides = append(overrides, taskcore.WithTags(req.Tags))
	}
	if req.RetryInterval != "" || req.RetryMaxAttempts != nil {
		if req.RetryInterval == "" || req.RetryMaxAttempts == nil {
			http.Error(w, "retryInterval and retryMaxAttempts must be set together", http.StatusBadRequest)
			return
		}
		overrides = append(overrides, taskcore.WithRetryPolicy(req.RetryInterval, *req.RetryMaxAttempts))
	}
	if req.DelayMs > 0 {
		overrides = append(overrides, taskcore.WithStartedAt(time.Now().Add(time.Duration(req.DelayMs)*time.Millisecond)))
	}
	params := &taskgen.StressProbeParameters{
		JobID:   req.JobID,
		SleepMs: req.SleepMs,
		Group:   req.Group,
	}
	if req.FailMode != "" {
		params.FailMode = &req.FailMode
	}
	if req.SignalBaseURL != "" {
		params.SignalBaseURL = &req.SignalBaseURL
	}
	if req.SignalIntervalMs > 0 {
		params.SignalIntervalMs = &req.SignalIntervalMs
	}
	taskID, err := a.runner.RunStressProbe(r.Context(), params, overrides...)
	if err != nil {
		writeInternalError(w, "RunStressProbe", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskID": taskID})
}

func (a *app) handleCancelObservableProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req cancelObservableProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TaskName == "" {
		http.Error(w, "taskName is required", http.StatusBadRequest)
		return
	}
	if req.Group == "" {
		http.Error(w, "group is required", http.StatusBadRequest)
		return
	}
	uniqueTag := req.UniqueTag
	if uniqueTag == "" {
		uniqueTag = req.TaskName
	}
	overrides := []taskcore.TaskOverride{taskcore.WithUniqueTag(uniqueTag)}
	if len(req.Labels) > 0 {
		overrides = append(overrides, taskcore.WithLabels(req.Labels))
	}
	if len(req.Tags) > 0 {
		overrides = append(overrides, taskcore.WithTags(req.Tags))
	}
	params := &taskgen.CancelObservableProbeParameters{Group: req.Group}
	if req.SignalBaseURL != "" {
		params.SignalBaseURL = &req.SignalBaseURL
	}
	if req.SignalIntervalMs > 0 {
		params.SignalIntervalMs = &req.SignalIntervalMs
	}
	taskID, err := a.runner.RunCancelObservableProbe(r.Context(), params, overrides...)
	if err != nil {
		writeInternalError(w, "RunCancelObservableProbe", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskID": taskID})
}

func (a *app) handlePauseTask(w http.ResponseWriter, r *http.Request) {
	a.handleTaskControl(w, r, func(ctx context.Context, uniqueTag string) error {
		return a.controlPlane.PauseTaskByUniqueTag(ctx, uniqueTag)
	})
}

func (a *app) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	a.handleTaskControl(w, r, func(ctx context.Context, uniqueTag string) error {
		return a.controlPlane.CancelTaskByUniqueTag(ctx, uniqueTag)
	})
}

func (a *app) handleResumeTask(w http.ResponseWriter, r *http.Request) {
	a.handleTaskControl(w, r, func(ctx context.Context, uniqueTag string) error {
		return a.controlPlane.ResumeTaskByUniqueTag(ctx, uniqueTag)
	})
}

func (a *app) handlePauseTasksByTags(w http.ResponseWriter, r *http.Request) {
	a.handleTaskTagsControl(w, r, func(ctx context.Context, tags []string, exceptTagSets [][]string) error {
		return a.controlPlane.PauseTasksByTags(ctx, tags, exceptTagSets...)
	})
}

func (a *app) handleCancelTasksByTags(w http.ResponseWriter, r *http.Request) {
	a.handleTaskTagsControl(w, r, func(ctx context.Context, tags []string, exceptTagSets [][]string) error {
		return a.controlPlane.CancelTasksByTags(ctx, tags, exceptTagSets...)
	})
}

func (a *app) handleResumeTasksByTags(w http.ResponseWriter, r *http.Request) {
	a.handleTaskTagsControl(w, r, func(ctx context.Context, tags []string, exceptTagSets [][]string) error {
		return a.controlPlane.ResumeTasksByTags(ctx, tags, exceptTagSets...)
	})
}

func (a *app) handleTaskControl(w http.ResponseWriter, r *http.Request, fn func(context.Context, string) error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req taskControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.UniqueTag == "" {
		http.Error(w, "uniqueTag is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chaosControlRequestTimeout)
	defer cancel()
	if err := fn(ctx, req.UniqueTag); err != nil {
		writeInternalError(w, "TaskControl", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleTaskTagsControl(w http.ResponseWriter, r *http.Request, fn func(context.Context, []string, [][]string) error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req taskTagsControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Tags) == 0 {
		http.Error(w, "tags are required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), chaosControlRequestTimeout)
	defer cancel()
	if err := fn(ctx, req.Tags, req.ExceptTagSets); err != nil {
		writeInternalError(w, "TaskTagsControl", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *app) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req runtimeConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	labels := make([]string, 0, len(req.LabelWeights))
	weights := make([]int32, 0, len(req.LabelWeights))
	for label, weight := range req.LabelWeights {
		labels = append(labels, label)
		weights = append(weights, weight)
	}
	ctx, cancel := context.WithTimeout(r.Context(), chaosControlRequestTimeout)
	defer cancel()
	taskID, err := a.controlPlane.StartUpdateWorkerRuntimeConfig(ctx, &ctrl.UpdateWorkerRuntimeConfigRequest{
		MaxStrictPercentage: &req.MaxStrictPercentage,
		DefaultWeight:       &req.DefaultWeight,
		Labels:              labels,
		Weights:             weights,
	})
	if err != nil {
		writeInternalError(w, "StartUpdateWorkerRuntimeConfig", err)
		return
	}
	writeJSON(w, http.StatusOK, runtimeConfigResponse{TaskID: taskID})
}

func writeInternalError(w http.ResponseWriter, op string, err error) {
	if err == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	log.Printf("control plane error op=%s err=%v", op, err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
