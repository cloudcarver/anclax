package chaos

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultPostgresImage = "postgres:15"
	defaultRuntimeImage  = "alpine:3.20"
	controlPlanePort     = 18080
)

type RunConfig struct {
	RunID             string
	Seed              int64
	ArtifactDir       string
	KeepArtifacts     bool
	KeepContainers    bool
	PostgresImage     string
	RuntimeImage      string
	ControlPlanePort  int
	PostgresUser      string
	PostgresPassword  string
	PostgresDatabase  string
	WorkerConcurrency int
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	LockTTL           time.Duration
	LockRefresh       time.Duration
	RuntimeConfigPoll time.Duration
}

func DefaultRunConfig() RunConfig {
	return RunConfig{
		RunID:             defaultRunID(),
		PostgresImage:     defaultPostgresImage,
		RuntimeImage:      defaultRuntimeImage,
		ControlPlanePort:  controlPlanePort,
		PostgresUser:      "postgres",
		PostgresPassword:  "postgres",
		PostgresDatabase:  "postgres",
		WorkerConcurrency: 2,
		PollInterval:      20 * time.Millisecond,
		HeartbeatInterval: 200 * time.Millisecond,
		LockTTL:           600 * time.Millisecond,
		LockRefresh:       100 * time.Millisecond,
		RuntimeConfigPoll: 200 * time.Millisecond,
	}
}

func (c RunConfig) Validate() error {
	if strings.TrimSpace(c.RunID) == "" {
		return fmt.Errorf("run id is required")
	}
	if c.PostgresImage == "" {
		return fmt.Errorf("postgres image is required")
	}
	if c.RuntimeImage == "" {
		return fmt.Errorf("runtime image is required")
	}
	if c.ControlPlanePort <= 0 {
		return fmt.Errorf("control plane port must be positive")
	}
	if c.PostgresUser == "" || c.PostgresPassword == "" || c.PostgresDatabase == "" {
		return fmt.Errorf("postgres credentials are required")
	}
	if c.WorkerConcurrency <= 0 {
		return fmt.Errorf("worker concurrency must be positive")
	}
	if c.PollInterval <= 0 || c.HeartbeatInterval <= 0 || c.LockTTL <= 0 || c.LockRefresh <= 0 {
		return fmt.Errorf("worker timings must be positive")
	}
	if c.ArtifactDir != "" && !filepath.IsAbs(c.ArtifactDir) {
		return fmt.Errorf("artifact dir must be absolute: %s", c.ArtifactDir)
	}
	return nil
}

func (c RunConfig) ensureArtifactDir() (string, error) {
	if c.ArtifactDir != "" {
		if err := os.MkdirAll(c.ArtifactDir, 0o755); err != nil {
			return "", err
		}
		return c.ArtifactDir, nil
	}
	return os.MkdirTemp("", "anclax-taskcore-chaos-"+c.RunID+"-")
}

func defaultRunID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func prefixedName(runID string, role string) string {
	return fmt.Sprintf("anclax-longevity-%s-%s", runID, role)
}
