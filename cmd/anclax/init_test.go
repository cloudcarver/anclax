package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInitializedProject(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and tests the scaffold as a separate Go module")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := initFiles(dir, "example.com/scaffold"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "anchor.yaml")); !os.IsNotExist(err) {
		t.Fatalf("obsolete config survived in scaffold: %v", err)
	}
	for _, args := range [][]string{
		{"mod", "edit", "-replace=github.com/cloudcarver/anclax=" + root},
		{"test", "-mod=mod", "./..."},
	} {
		cmd := exec.CommandContext(t.Context(), "go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
	}
}
