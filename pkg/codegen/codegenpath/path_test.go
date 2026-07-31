package codegenpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConstrainsPathsToWorkdir(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{workdir, outside, filepath.Join(workdir, "app", "service")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(workdir, "app"), filepath.Join(workdir, "internal")); err != nil {
		t.Fatalf("create internal symlink: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "nested service path", path: "app/service/zgen/spec_gen.go"},
		{name: "internal symlink", path: "internal/service/zgen/spec_gen.go"},
		{name: "absolute", path: filepath.Join(workdir, "generated.go"), wantErr: true},
		{name: "lexical escape", path: filepath.Join("..", "outside", "sentinel"), wantErr: true},
		{name: "escaping symlink", path: filepath.Join("escape", "sentinel"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(workdir, tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Resolve(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
	if err := MkdirAll(workdir, filepath.Join("internal", "service", "zgen"), 0755); err != nil {
		t.Fatalf("MkdirAll() rejected an absolute symlink that stays in workdir: %v", err)
	}
	if err := WriteFile(workdir, filepath.Join("internal", "service", "zgen", "generated.go"), []byte("generated"), 0644); err != nil {
		t.Fatalf("WriteFile() rejected an absolute symlink that stays in workdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "app", "service", "zgen", "generated.go")); err != nil {
		t.Fatalf("stat generated file through internal symlink target: %v", err)
	}
}

func TestResolveReadPreservesExternalInputCompatibility(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside", "spec.yaml")
	if err := os.MkdirAll(workdir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	for _, input := range []string{outside, filepath.Join("..", "outside", "spec.yaml")} {
		resolved, err := ResolveRead(workdir, input)
		if err != nil {
			t.Fatalf("ResolveRead(%q) error: %v", input, err)
		}
		if resolved != outside {
			t.Fatalf("ResolveRead(%q) = %q, want %q", input, resolved, outside)
		}
	}
}

func TestRootedMutationsDoNotFollowEscapingSymlinks(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(workdir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	if err := WriteFile(workdir, filepath.Join("escape", "sentinel"), []byte("overwrite"), 0644); err == nil {
		t.Fatal("WriteFile() unexpectedly followed an escaping symlink")
	}
	if err := RemoveAll(workdir, "escape"); err != nil {
		t.Fatalf("RemoveAll() could not safely remove an escaping symlink entry: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workdir, "escape")); !os.IsNotExist(err) {
		t.Fatalf("escaping symlink entry still exists, error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
		t.Fatalf("recreate escaping symlink: %v", err)
	}
	source := filepath.Join(workdir, "source")
	if err := os.WriteFile(source, []byte("source"), 0644); err != nil {
		t.Fatalf("write rename source: %v", err)
	}
	if err := Rename(workdir, "source", filepath.Join("escape", "moved")); err == nil {
		t.Fatal("Rename() unexpectedly accepted an escaping destination parent")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("rename source changed after rejected destination: %v", err)
	}
	raw, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(raw) != "keep" {
		t.Fatalf("sentinel = %q, want keep", raw)
	}
}

func TestResolvePatternRejectsUnsafeRoots(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(workdir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	for _, pattern := range []string{
		filepath.Join("..", "outside", "*"),
		filepath.Join("escape", "*"),
		".",
	} {
		if _, err := ResolvePattern(workdir, pattern); err == nil {
			t.Errorf("ResolvePattern(%q) unexpectedly succeeded", pattern)
		}
	}
	if _, err := ResolvePattern(workdir, filepath.Join("app", "*", "zgen", "*.go")); err != nil {
		t.Fatalf("ResolvePattern() rejected a nested service pattern: %v", err)
	}
	if _, err := ResolvePattern(workdir, "escape"); err != nil {
		t.Fatalf("ResolvePattern() rejected a safely removable final symlink: %v", err)
	}
}

func TestValidateTreeRejectsEscapingDescendantSymlink(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	output := filepath.Join(workdir, "pkg", "zgen")
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "sentinel"), filepath.Join(output, "generated.go")); err != nil {
		t.Fatalf("create escaping output symlink: %v", err)
	}
	if err := ValidateTree(workdir, filepath.Join("pkg", "zgen")); err == nil {
		t.Fatal("ValidateTree() unexpectedly accepted an escaping descendant symlink")
	}
}

func TestValidateTreeFollowsInternalDirectorySymlinks(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	workdir := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	output := filepath.Join(workdir, "pkg", "zgen")
	internal := filepath.Join(workdir, "internal")
	for _, dir := range []string{output, internal, outside} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(internal, filepath.Join(output, "nested")); err != nil {
		t.Fatalf("create internal directory symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "sentinel"), filepath.Join(internal, "generated.go")); err != nil {
		t.Fatalf("create escaping nested symlink: %v", err)
	}
	if err := ValidateTree(workdir, filepath.Join("pkg", "zgen")); err == nil {
		t.Fatal("ValidateTree() unexpectedly skipped an escaping symlink beneath an internal directory symlink")
	}
}
