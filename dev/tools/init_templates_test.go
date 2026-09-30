package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyToInitFilesRemovesStaleAndExcludedFiles(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write := func(root, name, contents string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(src, "go.mod", "module myexampleapp\n")
	write(src, "pkg/model.go", "package model\n")
	write(src, "anclax.yaml", "schemas: {}\n")
	write(src, "dev/app.yaml", "anclax: {}\n")
	write(src, "app.yaml", "local configuration")
	write(src, "go.sum", "excluded")
	write(src, ".anclax/bin/tool", "excluded")
	write(dst, "anchor.yaml", "stale")
	write(dst, "old/model.go.embed", "stale")
	write(dst, ".anclax/bin/tool", "stale")
	write(dst, "go.sum", "stale")
	write(dst, "app.yaml", "stale")

	excluded := []string{".anclax", "go.sum", "app.yaml"}
	if err := CopyToInitFiles(src, dst, excluded); err != nil {
		t.Fatal(err)
	}
	for source, target := range map[string]string{
		"go.mod": "go.mod.embed", "pkg/model.go": "pkg/model.go.embed", "anclax.yaml": "anclax.yaml",
		"dev/app.yaml": "dev/app.yaml",
	} {
		want, err := os.ReadFile(filepath.Join(src, source))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dst, target))
		if err != nil || string(got) != string(want) {
			t.Fatalf("template %s = %q, %v; want %q", target, got, err, want)
		}
	}
	for _, stale := range []string{"anchor.yaml", "old", ".anclax", "go.sum", "app.yaml"} {
		if _, err := os.Stat(filepath.Join(dst, stale)); !os.IsNotExist(err) {
			t.Fatalf("stale path %s survived: %v", stale, err)
		}
	}
	if err := os.Remove(filepath.Join(src, "pkg/model.go")); err != nil {
		t.Fatal(err)
	}
	if err := CopyToInitFiles(src, dst, excluded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "pkg/model.go.embed")); !os.IsNotExist(err) {
		t.Fatalf("deleted source survived regeneration: %v", err)
	}
}
