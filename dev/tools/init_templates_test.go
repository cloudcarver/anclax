package tools

import (
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
		t.Skipf("symbolic links are not supported: %v", err)
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
