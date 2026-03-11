package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/taskcore/ctrl"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

type stressProbeRequest struct {
	TaskName  string   `json:"taskName"`
	JobID     int64    `json:"jobID"`
	SleepMs   int32    `json:"sleepMs"`
	Group     string   `json:"group"`
	Labels    []string `json:"labels,omitempty"`
	DelayMs   int32    `json:"delayMs,omitempty"`
	UniqueTag string   `json:"uniqueTag,omitempty"`
}

type runtimeConfigRequest struct {
	MaxStrictPercentage int32            `json:"maxStrictPercentage"`
	DefaultWeight       int32            `json:"defaultWeight"`
	LabelWeights        map[string]int32 `json:"labelWeights,omitempty"`
}

type app struct {
	store        taskcore.TaskStoreInterface
	runner       taskgen.TaskRunner
	controlPlane *ctrl.WorkerControlPlane
	model        model.ModelInterface
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

	a := &app{store: store, runner: runner, controlPlane: controlPlane, model: m}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/tasks/stress-probe", a.handleStressProbe)
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
	if req.DelayMs > 0 {
		overrides = append(overrides, taskcore.WithStartedAt(time.Now().Add(time.Duration(req.DelayMs)*time.Millisecond)))
	}
	taskID, err := a.runner.RunStressProbe(r.Context(), &taskgen.StressProbeParameters{
		JobID:   req.JobID,
		SleepMs: req.SleepMs,
		Group:   req.Group,
	}, overrides...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskID": taskID})
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
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	err := a.controlPlane.UpdateWorkerRuntimeConfig(ctx, &ctrl.UpdateWorkerRuntimeConfigRequest{
		MaxStrictPercentage: &req.MaxStrictPercentage,
		DefaultWeight:       &req.DefaultWeight,
		Labels:              labels,
		Weights:             weights,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
