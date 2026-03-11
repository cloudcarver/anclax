package chaos

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type builtBinaries struct {
	Dir          string
	ControlPlane string
	Worker       string
}

func buildChaosBinaries(ctx context.Context, artifactDir string) (*builtBinaries, error) {
	binDir := filepath.Join(artifactDir, "bin")
	moduleDir, err := moduleRootDir(ctx)
	if err != nil {
		return nil, err
	}
	if err := runGoBuild(ctx, moduleDir, binDir, "taskcore-chaos-controlplane", "./pkg/taskcore/chaos/cmd/controlplane"); err != nil {
		return nil, err
	}
	if err := runGoBuild(ctx, moduleDir, binDir, "taskcore-chaos-worker", "./pkg/taskcore/chaos/cmd/worker"); err != nil {
		return nil, err
	}
	return &builtBinaries{
		Dir:          binDir,
		ControlPlane: filepath.Join(binDir, "taskcore-chaos-controlplane"),
		Worker:       filepath.Join(binDir, "taskcore-chaos-worker"),
	}, nil
}

func runGoBuild(ctx context.Context, moduleDir string, binDir string, output string, pkg string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(binDir, output), pkg)
	cmd.Dir = moduleDir
	cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0")
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s failed: %w: %s", pkg, err, string(combined))
	}
	return nil
}

func moduleRootDir(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}")
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go list -m failed: %w: %s", err, string(combined))
	}
	return strings.TrimSpace(string(combined)), nil
}
