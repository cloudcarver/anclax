package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudcarver/anclax/pkg/app/closer"
	"github.com/cloudcarver/anclax/pkg/asynctask"
	"github.com/cloudcarver/anclax/pkg/config"
	"github.com/cloudcarver/anclax/pkg/globalctx"
	"github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/cloudcarver/anclax/pkg/taskcore/worker"
	"github.com/cloudcarver/anclax/pkg/zcore/model"
	"github.com/cloudcarver/anclax/pkg/zgen/taskgen"
)

func main() {
	dsn := os.Getenv("CHAOS_DSN")
	if dsn == "" {
		log.Fatal("CHAOS_DSN is required")
	}
	name := os.Getenv("CHAOS_WORKER_NAME")
	if name == "" {
		log.Fatal("CHAOS_WORKER_NAME is required")
	}
	labels := splitCSV(os.Getenv("CHAOS_WORKER_LABELS"))
	cfg := &config.Config{Pg: config.Pg{DSN: &dsn}}
	cfg.Worker.Labels = labels
	cfg.Worker.Concurrency = intPtr(envInt("CHAOS_WORKER_CONCURRENCY", 2))
	cfg.Worker.PollInterval = durationPtr(envInt("CHAOS_POLL_INTERVAL_MS", 20))
	cfg.Worker.HeartbeatInterval = durationPtr(envInt("CHAOS_HEARTBEAT_INTERVAL_MS", 200))
	cfg.Worker.LockTTL = durationPtr(envInt("CHAOS_LOCK_TTL_MS", 600))
	cfg.Worker.LockRefreshInterval = durationPtr(envInt("CHAOS_LOCK_REFRESH_MS", 100))
	cfg.Worker.RuntimeConfigPollInterval = durationPtr(envInt("CHAOS_RUNTIME_CONFIG_POLL_MS", 200))

	libCfg := config.DefaultLibConfig()
	cm := closer.NewCloserManager()
	defer cm.Close()

	m, err := model.NewModel(cfg, libCfg, cm)
	if err != nil {
		log.Fatal(err)
	}
	taskStore := store.NewTaskStore(m)
	runner := taskgen.NewTaskRunner(taskStore)
	executor := asynctask.NewExecutor(cfg, m, runner)
	handler := taskgen.NewTaskHandler(executor)
	gctx := globalctx.New()
	w, err := worker.NewWorkerFromConfig(gctx, cfg, m, handler)
	if err != nil {
		log.Fatal(err)
	}
	w.RegisterTaskHandler(asynctask.NewWorkerControlTaskHandler(w))
	executor.SetLocalWorker(w)
	log.Printf("worker %s starting labels=%v", name, labels)
	w.Start()
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func intPtr(v int) *int { return &v }

func durationPtr(ms int) *time.Duration {
	d := time.Duration(ms) * time.Millisecond
	return &d
}
