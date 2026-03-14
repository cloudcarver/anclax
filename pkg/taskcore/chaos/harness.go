package chaos

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Harness struct {
	cfg              RunConfig
	artifactDir      string
	report           *Report
	binaries         *builtBinaries
	networkName      string
	postgresName     string
	controlPlaneName string
	postgresHostPort int
	controlHostPort  int
	postgresHostDSN  string
	postgresInnerDSN string
	controlPlaneURL  string
	signalService    *SignalService
	signalClient     *SignalClient
	inspector        *Inspector
	controlClient    *ControlPlaneClient
	mu               sync.Mutex
	workers          map[string]*WorkerHandle
}

type WorkerHandle struct {
	Name          string
	ContainerName string
	Labels        []string
	Running       bool
	Removed       bool
}

func NewHarness(cfg RunConfig) (*Harness, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	artifactDir, err := cfg.ensureArtifactDir()
	if err != nil {
		return nil, err
	}
	h := &Harness{
		cfg:              cfg,
		artifactDir:      artifactDir,
		report:           NewReport(cfg.RunID, cfg.Seed, artifactDir),
		networkName:      prefixedName(cfg.RunID, "net"),
		postgresName:     prefixedName(cfg.RunID, "postgres"),
		controlPlaneName: prefixedName(cfg.RunID, "control"),
		workers:          map[string]*WorkerHandle{},
	}
	return h, nil
}

func (h *Harness) ArtifactDir() string { return h.artifactDir }
func (h *Harness) Report() *Report     { return h.report }
func (h *Harness) User() *User {
	signalBaseURL := fmt.Sprintf("http://%s:%d", h.controlPlaneName, h.cfg.ControlPlanePort)
	return &User{Control: h.controlClient, Signals: NewSignalClient(h.controlPlaneURL), SignalBaseURL: signalBaseURL, DB: h.inspector, Report: h.report}
}
func (h *Harness) Inspector() *Inspector { return h.inspector }

func (h *Harness) WaitWorkerOnline(ctx context.Context, name string, expected bool, timeout time.Duration) error {
	if h.inspector == nil {
		return fmt.Errorf("inspector not initialized")
	}
	return h.inspector.WaitWorkerOnline(ctx, name, expected, timeout)
}

func (h *Harness) Start(ctx context.Context) error {
	if !dockerAvailable() {
		return fmt.Errorf("docker not available")
	}
	var err error
	h.binaries, err = buildChaosBinaries(ctx, h.artifactDir)
	if err != nil {
		return err
	}
	h.report.AddEvent("harness.build", "binaries", "built helper binaries", map[string]any{"dir": h.binaries.Dir})

	h.postgresHostPort, err = findFreePort()
	if err != nil {
		return err
	}
	h.controlHostPort, err = findFreePort()
	if err != nil {
		return err
	}

	h.postgresHostDSN = fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", h.cfg.PostgresUser, h.cfg.PostgresPassword, h.postgresHostPort, h.cfg.PostgresDatabase)
	h.postgresInnerDSN = fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable", h.cfg.PostgresUser, h.cfg.PostgresPassword, h.postgresName, h.cfg.PostgresDatabase)
	h.controlPlaneURL = fmt.Sprintf("http://127.0.0.1:%d", h.controlHostPort)
	h.controlClient = NewControlPlaneClient(h.controlPlaneURL)
	h.signalService = NewSignalService()
	if err := h.signalService.Start(); err != nil {
		return err
	}
	h.signalClient = NewSignalClient(h.signalService.HostURL())
	if err := h.signalClient.Health(ctx); err != nil {
		return err
	}
	h.report.AddEvent("harness.signal_service.start", "signals", "started signal service", map[string]any{"hostURL": h.signalService.HostURL(), "containerBaseURL": h.signalService.ContainerBaseURL()})

	if _, err := dockerCommand(ctx, "network", "create", h.networkName); err != nil {
		return err
	}
	h.report.AddEvent("docker.network.create", h.networkName, "created docker network", nil)

	if err := h.startPostgres(ctx); err != nil {
		return err
	}
	if err := h.startControlPlane(ctx); err != nil {
		return err
	}
	inspector, err := NewInspector(ctx, h.postgresHostDSN)
	if err != nil {
		return err
	}
	h.inspector = inspector
	return nil
}

func (h *Harness) Close(ctx context.Context) error {
	if h.inspector != nil {
		h.inspector.Close()
	}
	if h.signalService != nil {
		_ = h.signalService.Close(ctx)
	}
	if h.report != nil {
		_ = h.report.Write()
	}
	if h.cfg.KeepContainers {
		return nil
	}
	workerNames := h.workerNames()
	for _, name := range workerNames {
		dockerIgnore(ctx, "rm", "-f", h.workers[name].ContainerName)
	}
	dockerIgnore(ctx, "rm", "-f", h.controlPlaneName)
	dockerIgnore(ctx, "rm", "-f", h.postgresName)
	dockerIgnore(ctx, "network", "rm", h.networkName)
	if !h.cfg.KeepArtifacts {
		return nil
	}
	return nil
}

func (h *Harness) startPostgres(ctx context.Context) error {
	args := []string{"run", "-d", "--name", h.postgresName, "--network", h.networkName,
		"-e", "POSTGRES_PASSWORD=" + h.cfg.PostgresPassword,
		"-e", "POSTGRES_USER=" + h.cfg.PostgresUser,
		"-e", "POSTGRES_DB=" + h.cfg.PostgresDatabase,
		"-p", fmt.Sprintf("%d:5432", h.postgresHostPort),
		h.cfg.PostgresImage,
	}
	if _, err := dockerCommand(ctx, args...); err != nil {
		return err
	}
	h.report.AddEvent("docker.container.start", h.postgresName, "started postgres", map[string]any{"hostPort": h.postgresHostPort})
	return h.waitPostgres(ctx, 30*time.Second)
}

func (h *Harness) startControlPlane(ctx context.Context) error {
	args := []string{"run", "-d", "--name", h.controlPlaneName, "--network", h.networkName,
		"-p", fmt.Sprintf("%d:%d", h.controlHostPort, h.cfg.ControlPlanePort),
	}
	args = append(args, hostGatewayAlias("host.docker.internal")...)
	args = append(args, quotedEnv(map[string]string{
		"CHAOS_DSN":       h.postgresInnerDSN,
		"CHAOS_HTTP_ADDR": fmt.Sprintf(":%d", h.cfg.ControlPlanePort),
	})...)
	args = append(args, bindMount(h.binaries.Dir, "/mnt")...)
	args = append(args, h.cfg.RuntimeImage)
	args = append(args, containerBinaryCommand("/mnt", filepath.Base(h.binaries.ControlPlane))...)
	if _, err := dockerCommand(ctx, args...); err != nil {
		return err
	}
	h.report.AddEvent("docker.container.start", h.controlPlaneName, "started control plane", map[string]any{"hostPort": h.controlHostPort})
	return h.waitControlPlane(ctx, 30*time.Second)
}

func (h *Harness) waitPostgres(ctx context.Context, timeout time.Duration) error {
	inspector, err := NewInspector(ctx, h.postgresHostDSN)
	if err == nil {
		inspector.Close()
		return nil
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		inspector, err := NewInspector(ctx, h.postgresHostDSN)
		if err == nil {
			inspector.Close()
			return nil
		}
	}
	return fmt.Errorf("timeout waiting for postgres")
}

func (h *Harness) waitControlPlane(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		if err := h.controlClient.Health(ctx); err == nil {
			return nil
		}
	}
	return fmt.Errorf("timeout waiting for control plane")
}

func (h *Harness) StartWorker(ctx context.Context, name string, labels []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.workers[name]; ok {
		if existing.Running && !existing.Removed {
			return nil
		}
	}
	containerName := prefixedName(h.cfg.RunID, name)
	dockerIgnore(ctx, "rm", "-f", containerName)
	workerLabels := append([]string(nil), labels...)
	workerLabels = append(workerLabels, "chaos:"+name)
	args := []string{"run", "-d", "--name", containerName, "--network", h.networkName}
	args = append(args, hostGatewayAlias("host.docker.internal")...)
	signalBaseURL := fmt.Sprintf("http://%s:%d", h.controlPlaneName, h.cfg.ControlPlanePort)
	args = append(args, quotedEnv(map[string]string{
		"CHAOS_DSN":                    h.postgresInnerDSN,
		"CHAOS_WORKER_NAME":            name,
		"CHAOS_WORKER_LABELS":          strings.Join(workerLabels, ","),
		"CHAOS_SIGNAL_BASE_URL":        signalBaseURL,
		"CHAOS_WORKER_CONCURRENCY":     fmt.Sprintf("%d", h.cfg.WorkerConcurrency),
		"CHAOS_POLL_INTERVAL_MS":       intToString(int(h.cfg.PollInterval / time.Millisecond)),
		"CHAOS_HEARTBEAT_INTERVAL_MS":  intToString(int(h.cfg.HeartbeatInterval / time.Millisecond)),
		"CHAOS_LOCK_TTL_MS":            intToString(int(h.cfg.LockTTL / time.Millisecond)),
		"CHAOS_LOCK_REFRESH_MS":        intToString(int(h.cfg.LockRefresh / time.Millisecond)),
		"CHAOS_RUNTIME_CONFIG_POLL_MS": intToString(int(h.cfg.RuntimeConfigPoll / time.Millisecond)),
	})...)
	args = append(args, bindMount(h.binaries.Dir, "/mnt")...)
	args = append(args, h.cfg.RuntimeImage)
	args = append(args, containerBinaryCommand("/mnt", filepath.Base(h.binaries.Worker))...)
	if _, err := dockerCommand(ctx, args...); err != nil {
		return err
	}
	h.workers[name] = &WorkerHandle{Name: name, ContainerName: containerName, Labels: labels, Running: true}
	h.report.AddEvent("docker.container.start", containerName, "started worker", map[string]any{"labels": workerLabels})
	return nil
}

func (h *Harness) StopWorker(ctx context.Context, name string, remove bool) error {
	h.mu.Lock()
	worker, ok := h.workers[name]
	h.mu.Unlock()
	if !ok || worker == nil {
		return nil
	}
	cmd := []string{"stop", worker.ContainerName}
	if remove {
		cmd = []string{"rm", "-f", worker.ContainerName}
	}
	if _, err := dockerCommand(ctx, cmd...); err != nil {
		return err
	}
	h.mu.Lock()
	worker.Running = false
	worker.Removed = remove
	h.mu.Unlock()
	h.report.AddEvent("docker.container.stop", worker.ContainerName, "stopped worker", map[string]any{"remove": remove})
	return nil
}

func (h *Harness) RestartPostgres(ctx context.Context) error {
	if _, err := dockerCommand(ctx, "restart", h.postgresName); err != nil {
		return err
	}
	h.report.AddEvent("docker.container.restart", h.postgresName, "restarted postgres", nil)
	return h.waitPostgres(ctx, 30*time.Second)
}

func (h *Harness) StopControlPlane(ctx context.Context) error {
	if _, err := dockerCommand(ctx, "stop", h.controlPlaneName); err != nil {
		return err
	}
	h.report.AddEvent("docker.container.stop", h.controlPlaneName, "stopped control plane", nil)
	return nil
}

func (h *Harness) StartControlPlane(ctx context.Context) error {
	dockerIgnore(ctx, "rm", "-f", h.controlPlaneName)
	return h.startControlPlane(ctx)
}

func (h *Harness) CollectDiagnostics(ctx context.Context, failure error) error {
	if failure != nil {
		h.report.MarkFailure(failure.Error())
	}
	dockerDir := filepath.Join(h.artifactDir, "docker")
	if err := os.MkdirAll(dockerDir, 0o755); err != nil {
		return err
	}
	containers := append([]string{h.postgresName, h.controlPlaneName}, h.workerContainerNames()...)
	for _, name := range containers {
		_ = dockerInspectToFile(ctx, name, filepath.Join(dockerDir, name+"-inspect.json"))
		_ = dockerLogsToFile(ctx, name, filepath.Join(dockerDir, name+".log"))
	}
	if h.inspector != nil {
		_ = h.inspector.DumpDiagnostics(ctx, h.artifactDir, "LONG-")
	}
	return h.report.Write()
}

func (h *Harness) workerNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.workers))
	for name := range h.workers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (h *Harness) workerContainerNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.workers))
	for _, worker := range h.workers {
		out = append(out, worker.ContainerName)
	}
	sort.Strings(out)
	return out
}

func (h *Harness) WaitHTTPUnavailable(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/healthz")
		if err != nil {
			return nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("http endpoint %s remained available", url)
}
