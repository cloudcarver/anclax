//go:build darwin || linux

package tools

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestCopyToInitFilesRejectsSpecialSourceEntries(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	err := CopyToInitFiles(src, dst, nil)
	assertErrorContains(t, err, "not a regular file")
	assertPathDoesNotExist(t, filepath.Join(dst, "pipe"))
}
