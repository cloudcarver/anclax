package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyToInitFilesCopiesFilesAndAddsEmbedSuffix(t *testing.T) {
	workdir := t.TempDir()
	src := filepath.Join(workdir, "src")
	dst := filepath.Join(workdir, "dst")
	mustMkdirAll(t, filepath.Join(src, "nested"))
	mustMkdirAll(t, dst)
	mustWriteFile(t, filepath.Join(src, "main.go"), "package main\n")
	mustWriteFile(t, filepath.Join(src, "go.mod"), "module example.com/test\n")
	mustWriteFile(t, filepath.Join(src, "nested", "config.txt"), "enabled=true\n")
	mustWriteFile(t, filepath.Join(src, "go.sum"), "excluded\n")
	mustWriteFile(t, filepath.Join(dst, "main.go.embed"), "stale\n")

	if err := CopyToInitFiles(src, dst, []string{"go.sum"}); err != nil {
		t.Fatalf("CopyToInitFiles() error = %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "main.go.embed"), "package main\n")
	assertFileContent(t, filepath.Join(dst, "go.mod.embed"), "module example.com/test\n")
	assertFileContent(t, filepath.Join(dst, "nested", "config.txt"), "enabled=true\n")
	assertPathDoesNotExist(t, filepath.Join(dst, "main.go"))
	assertPathDoesNotExist(t, filepath.Join(dst, "go.mod"))
	assertPathDoesNotExist(t, filepath.Join(dst, "go.sum"))
}

func TestCopyToInitFilesRejectsSourceSymlink(t *testing.T) {
	for _, tc := range []struct {
		name       string
		makeTarget func(t *testing.T, path string)
	}{
		{
			name: "file",
			makeTarget: func(t *testing.T, path string) {
				mustWriteFile(t, path, "external secret\n")
			},
		},
		{
			name: "directory",
			makeTarget: func(t *testing.T, path string) {
				mustMkdirAll(t, path)
				mustWriteFile(t, filepath.Join(path, "secret.txt"), "external secret\n")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workdir := t.TempDir()
			src := filepath.Join(workdir, "src")
			dst := filepath.Join(workdir, "dst")
			outside := filepath.Join(workdir, "outside")
			mustMkdirAll(t, src)
			tc.makeTarget(t, outside)
			mustSymlink(t, outside, filepath.Join(src, "leak"))

			err := CopyToInitFiles(src, dst, nil)
			assertErrorContains(t, err, "symbolic link")
			assertErrorContains(t, err, "leak")
			assertPathDoesNotExist(t, filepath.Join(dst, "leak"))
		})
	}
}

func TestCopyToInitFilesRejectsDestinationFileSymlink(t *testing.T) {
	workdir := t.TempDir()
	src := filepath.Join(workdir, "src")
	dst := filepath.Join(workdir, "dst")
	outside := filepath.Join(workdir, "outside.txt")
	mustMkdirAll(t, src)
	mustMkdirAll(t, dst)
	mustWriteFile(t, filepath.Join(src, "payload.txt"), "replacement\n")
	mustWriteFile(t, outside, "original\n")
	mustSymlink(t, outside, filepath.Join(dst, "payload.txt"))

	err := CopyToInitFiles(src, dst, nil)
	assertErrorContains(t, err, "symbolic link")
	assertErrorContains(t, err, "payload.txt")
	assertFileContent(t, outside, "original\n")
}

func TestCopyToInitFilesRejectsDestinationParentSymlink(t *testing.T) {
	workdir := t.TempDir()
	src := filepath.Join(workdir, "src")
	dst := filepath.Join(workdir, "dst")
	outside := filepath.Join(workdir, "outside")
	mustMkdirAll(t, filepath.Join(src, "nested"))
	mustMkdirAll(t, dst)
	mustMkdirAll(t, outside)
	mustWriteFile(t, filepath.Join(src, "nested", "payload.txt"), "replacement\n")
	mustWriteFile(t, filepath.Join(outside, "payload.txt"), "original\n")
	mustSymlink(t, outside, filepath.Join(dst, "nested"))

	err := CopyToInitFiles(src, dst, nil)
	assertErrorContains(t, err, "symbolic link")
	assertErrorContains(t, err, "nested")
	assertFileContent(t, filepath.Join(outside, "payload.txt"), "original\n")
}

func TestCopyToInitFilesRejectsDestinationNonDirectoryParent(t *testing.T) {
	workdir := t.TempDir()
	src := filepath.Join(workdir, "src")
	dst := filepath.Join(workdir, "dst")
	mustMkdirAll(t, filepath.Join(src, "nested"))
	mustMkdirAll(t, dst)
	mustWriteFile(t, filepath.Join(src, "nested", "payload.txt"), "replacement\n")
	mustWriteFile(t, filepath.Join(dst, "nested"), "original\n")

	err := CopyToInitFiles(src, dst, nil)
	assertErrorContains(t, err, "not a directory")
	assertFileContent(t, filepath.Join(dst, "nested"), "original\n")
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("failed to create test symbolic link: %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	if string(got) != want {
		t.Errorf("content of %q = %q, want %q", path, got, want)
	}
}

func assertPathDoesNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("os.Lstat(%q) error = %v, want not exist", path, err)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want it to contain %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err, want)
	}
}

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
	write(src, ".env", "POSTGRES_PASSWORD=local-secret")
	write(src, ".env.local", "POSTGRES_PASSWORD=another-secret")
	write(src, "go.sum", "excluded")
	write(src, ".anclax/bin/tool", "excluded")
	write(dst, "anchor.yaml", "stale")
	write(dst, "old/model.go.embed", "stale")
	write(dst, ".anclax/bin/tool", "stale")
	write(dst, "go.sum", "stale")
	write(dst, "app.yaml", "stale")
	write(dst, ".env", "stale secret")
	write(dst, ".env.local", "stale secret")

	excluded := []string{".anclax", "go.sum", "app.yaml", ".env*"}
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
	for _, stale := range []string{"anchor.yaml", "old", ".anclax", "go.sum", "app.yaml", ".env", ".env.local"} {
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

func TestCopyToInitFilesRejectsWorkingDirectorySymlinkParents(t *testing.T) {
	for _, side := range []string{"source", "destination"} {
		for _, absolute := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/absolute=%t", side, absolute), func(t *testing.T) {
				project, outside := t.TempDir(), t.TempDir()
				t.Chdir(project)
				src, dst := "examples/simple", "cmd/templates"
				if side == "source" {
					mustMkdirAll(t, filepath.Join(outside, "simple"))
					mustWriteFile(t, filepath.Join(outside, "simple", "secret.txt"), "external secret")
					mustSymlink(t, outside, filepath.Join(project, "examples"))
				} else {
					mustMkdirAll(t, filepath.Join(project, src))
					mustWriteFile(t, filepath.Join(project, src, "payload.txt"), "replacement")
					mustMkdirAll(t, filepath.Join(outside, "templates"))
					mustWriteFile(t, filepath.Join(outside, "templates", "payload.txt"), "original")
					mustSymlink(t, outside, filepath.Join(project, "cmd"))
				}
				if absolute {
					src, dst = filepath.Join(project, src), filepath.Join(project, dst)
				}
				err := CopyToInitFiles(src, dst, nil)
				if side == "destination" {
					assertFileContent(t, filepath.Join(outside, "templates", "payload.txt"), "original")
				} else {
					assertPathDoesNotExist(t, filepath.Join(project, "cmd", "templates", "secret.txt"))
				}
				assertErrorContains(t, err, "symbolic link")
			})
		}
	}
}

func TestCopyToInitFilesAllowsAliasesAboveExplicitRoots(t *testing.T) {
	workdir := t.TempDir()
	realParent := filepath.Join(workdir, "real")
	alias := filepath.Join(workdir, "alias")
	mustMkdirAll(t, filepath.Join(realParent, "src"))
	mustWriteFile(t, filepath.Join(realParent, "src", "main.go"), "package main\n")
	mustSymlink(t, realParent, alias)
	if err := CopyToInitFiles(filepath.Join(alias, "src"), filepath.Join(alias, "new", "nested", "dst"), nil); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(realParent, "new", "nested", "dst", "main.go.embed"), "package main\n")
}

func TestCopyToInitFilesRejectsSymlinkedRoots(t *testing.T) {
	for _, side := range []string{"source", "destination"} {
		t.Run(side, func(t *testing.T) {
			workdir := t.TempDir()
			src, dst, outside := filepath.Join(workdir, "src"), filepath.Join(workdir, "dst"), filepath.Join(workdir, "outside")
			mustMkdirAll(t, outside)
			mustWriteFile(t, filepath.Join(outside, "payload.txt"), "original")
			if side == "source" {
				mustSymlink(t, outside, src)
			} else {
				mustMkdirAll(t, src)
				mustWriteFile(t, filepath.Join(src, "payload.txt"), "replacement")
				mustSymlink(t, outside, dst)
			}
			err := CopyToInitFiles(src, dst, nil)
			assertErrorContains(t, err, "symbolic link")
			assertFileContent(t, filepath.Join(outside, "payload.txt"), "original")
			if side == "source" {
				assertPathDoesNotExist(t, filepath.Join(dst, "payload.txt"))
			}
		})
	}
}

func TestCopyToInitFilesCleansStaleLinksWithoutFollowingThem(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "sentinel.txt"), "original")
	mustSymlink(t, outside, filepath.Join(dst, "stale-link"))
	mustMkdirAll(t, filepath.Join(dst, "stale-dir"))
	mustSymlink(t, outside, filepath.Join(dst, "stale-dir", "link"))
	if err := CopyToInitFiles(src, dst, nil); err != nil {
		t.Fatal(err)
	}
	assertPathDoesNotExist(t, filepath.Join(dst, "stale-link"))
	assertPathDoesNotExist(t, filepath.Join(dst, "stale-dir"))
	assertFileContent(t, filepath.Join(outside, "sentinel.txt"), "original")
}

func TestCopyToInitFilesPreservesPermissionsAndBreaksDestinationHardLinks(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteFile(t, filepath.Join(src, "payload.txt"), "replacement")
	mustWriteFile(t, outside, "original")
	if err := os.Chmod(outside, 0660); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dst, "payload.txt")
	if err := os.Link(outside, dest); err != nil {
		t.Fatal(err)
	}
	if err := CopyToInitFiles(src, dst, nil); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, outside, "original")
	assertFileContent(t, dest, "replacement")
	after, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("destination permissions = %o, want %o", after.Mode().Perm(), before.Mode().Perm())
	}
	if os.SameFile(before, after) {
		t.Fatal("destination still links to the external file")
	}
}
