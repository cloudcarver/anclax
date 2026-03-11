package chaos

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func dockerAvailable() bool {
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	return cmd.Run() == nil
}

func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected addr type %T", ln.Addr())
	}
	return addr.Port, nil
}

func dockerCommand(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("docker %s failed: %w: %s", strings.Join(args, " "), err, string(output))
	}
	return output, nil
}

func dockerIgnore(ctx context.Context, args ...string) {
	_, _ = dockerCommand(ctx, args...)
}

func dockerLogsToFile(ctx context.Context, container string, path string) error {
	output, err := dockerCommand(ctx, "logs", container)
	if err != nil && len(output) == 0 {
		return err
	}
	return os.WriteFile(path, output, 0o644)
}

func dockerInspectToFile(ctx context.Context, container string, path string) error {
	output, err := dockerCommand(ctx, "inspect", container)
	if err != nil {
		return err
	}
	return os.WriteFile(path, output, 0o644)
}

func quotedEnv(env map[string]string) []string {
	args := make([]string, 0, len(env)*2)
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	return args
}

func bindMount(src string, dst string) []string {
	return []string{"-v", src + ":" + dst}
}

func containerBinaryCommand(mountDir string, binaryName string) []string {
	return []string{filepath.Join(mountDir, binaryName)}
}

func intToString(v int) string {
	return strconv.Itoa(v)
}
