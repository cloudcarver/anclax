//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedSetupCreatesPrivateStableCredentials(t *testing.T) {
	projectDir := generateProjectForSecurityTest(t)
	run := func() {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "make", "setup")
		cmd.Dir = projectDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup failed: %v\n%s", err, output)
		}
	}
	run()
	appPath := filepath.Join(projectDir, "app.yaml")
	appDefaults, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(appDefaults) != readGeneratedFile(t, projectDir, "dev", "app.yaml") {
		t.Fatal("setup did not copy the development configuration")
	}
	customConfig := []byte("anclax:\n  enableSimpleAuth: false\n")
	if err := os.WriteFile(appPath, customConfig, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, ".env")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSuffix(strings.TrimPrefix(string(content), "POSTGRES_PASSWORD="), "\n")
	decoded, err := hex.DecodeString(password)
	if err != nil || len(decoded) != 32 {
		t.Fatal("setup did not produce a 256-bit hexadecimal password")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("local credentials have permissions %o, want 600", info.Mode().Perm())
	}
	run()
	preserved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, preserved) {
		t.Fatal("setup changed credentials for an existing database")
	}
	appConfig, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(appConfig, customConfig) {
		t.Fatal("setup overwrote customized application configuration")
	}
}

func TestGeneratedSetupDoesNotWriteCredentialsWhenRandomGenerationFails(t *testing.T) {
	projectDir := generateProjectForSecurityTest(t)
	if err := os.WriteFile(filepath.Join(projectDir, "app.yaml"), []byte("anclax: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	toolsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolsDir, "openssl"), []byte("#!/bin/sh\nprintf invoked > openssl-called\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "sh", "dev/setup.sh")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "PATH="+toolsDir)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("setup unexpectedly accepted failed random generation: %s", output)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "openssl-called")); err != nil {
		t.Fatalf("setup did not reach random generation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("setup left credentials after failed random generation: %v", err)
	}
}
