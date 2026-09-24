package codegenpath

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve returns an absolute path beneath workdir. Configured paths must be
// relative, must not lexically escape workdir, and must not traverse an
// existing symbolic link that resolves outside workdir.
func Resolve(workdir, path string) (string, error) {
	rel, err := relative(path)
	if err != nil {
		return "", err
	}

	root, err := canonicalRoot(workdir)
	if err != nil {
		return "", err
	}
	return resolveExisting(root, rel, path)
}

// ResolveSubpath is Resolve with an additional guard against selecting the
// workdir itself. It is intended for destructive directory operations.
func ResolveSubpath(workdir, path string) (string, error) {
	rel, err := relative(path)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", fmt.Errorf("path %q must select a child of workdir", path)
	}
	return Resolve(workdir, path)
}

// ResolveRead returns an absolute path for a read-only input. Unlike Resolve,
// it intentionally preserves the framework's support for inputs outside the
// workdir; callers must never use it for a mutation target.
func ResolveRead(workdir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	resolved, err := filepath.Abs(filepath.Join(normalizeWorkdir(workdir), path))
	if err != nil {
		return "", fmt.Errorf("resolve read-only path %q: %w", path, err)
	}
	return filepath.Clean(resolved), nil
}

// ResolvePattern validates a workdir-relative filepath.Glob pattern and
// returns an absolute pattern. Matches still need to be passed through
// RelEntry, because a wildcard can select a path beneath an escaping symlink.
func ResolvePattern(workdir, pattern string) (string, error) {
	rel, err := relative(pattern)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", fmt.Errorf("pattern %q must not select workdir", pattern)
	}

	prefix := filepath.Dir(pattern)
	if i := strings.IndexAny(pattern, "*?["); i >= 0 {
		prefix = filepath.Dir(pattern[:i])
	}
	if prefix == "" {
		prefix = "."
	}
	if _, err := Resolve(workdir, prefix); err != nil {
		return "", fmt.Errorf("pattern %q has an unsafe parent: %w", pattern, err)
	}

	root, err := canonicalRoot(workdir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, pattern), nil
}

// RelEntry returns a workdir-relative form of a filesystem entry after
// validating its parent, without following the final entry when it is a
// symbolic link. This is appropriate for operations that rename or remove the
// entry itself rather than reading or writing through it.
func RelEntry(workdir, path string) (string, error) {
	root, err := canonicalRoot(workdir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", fmt.Errorf("make path %q relative to workdir: %w", path, err)
	}
	rel, err = relative(rel)
	if err != nil {
		return "", err
	}
	if _, err := Resolve(workdir, filepath.Dir(rel)); err != nil {
		return "", err
	}
	return rel, nil
}

// Rel returns a workdir-relative form of path after checking both lexical and
// existing-symlink containment.
func Rel(workdir, path string) (string, error) {
	root, err := canonicalRoot(workdir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", fmt.Errorf("make path %q relative to workdir: %w", path, err)
	}
	if _, err := relative(rel); err != nil {
		return "", err
	}
	if _, err := Resolve(workdir, rel); err != nil {
		return "", err
	}
	return filepath.Clean(rel), nil
}

// MkdirAll creates a directory through an os.Root so symbolic-link changes
// cannot redirect the mutation outside workdir after validation.
func MkdirAll(workdir, path string, perm os.FileMode) error {
	if _, err := Resolve(workdir, path); err != nil {
		return err
	}
	rel, err := rootedRel(workdir, path, true)
	if err != nil {
		return err
	}
	root, err := openCanonicalRoot(workdir)
	if err != nil {
		return fmt.Errorf("open workdir: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll(rel, perm); err != nil {
		return fmt.Errorf("create %q beneath workdir: %w", path, err)
	}
	return nil
}

// WriteFile writes a file through an os.Root after safely creating its parent.
func WriteFile(workdir, path string, data []byte, perm os.FileMode) error {
	if _, err := Resolve(workdir, path); err != nil {
		return err
	}
	rel, err := rootedRel(workdir, path, true)
	if err != nil {
		return err
	}
	root, err := openCanonicalRoot(workdir)
	if err != nil {
		return fmt.Errorf("open workdir: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(rel), 0755); err != nil {
		return fmt.Errorf("create parent for %q beneath workdir: %w", path, err)
	}
	if err := root.WriteFile(rel, data, perm); err != nil {
		return fmt.Errorf("write %q beneath workdir: %w", path, err)
	}
	return nil
}

// RemoveAll recursively removes a child path through an os.Root.
func RemoveAll(workdir, path string) error {
	lexicalRel, err := relative(path)
	if err != nil {
		return err
	}
	if lexicalRel == "." {
		return fmt.Errorf("path %q must select a child of workdir", path)
	}
	rel, err := rootedRel(workdir, path, false)
	if err != nil {
		return err
	}
	root, err := openCanonicalRoot(workdir)
	if err != nil {
		return fmt.Errorf("open workdir: %w", err)
	}
	defer root.Close()
	if err := root.RemoveAll(rel); err != nil {
		return fmt.Errorf("remove %q beneath workdir: %w", path, err)
	}
	return nil
}

// MkdirTemp creates a randomly named child directory through an os.Root and
// returns its workdir-relative name.
func MkdirTemp(workdir, prefix string) (string, error) {
	if prefix == "" || filepath.IsAbs(prefix) || filepath.VolumeName(prefix) != "" || strings.ContainsAny(prefix, `/\`) {
		return "", fmt.Errorf("temporary directory prefix %q must be a single relative component", prefix)
	}
	root, err := openCanonicalRoot(workdir)
	if err != nil {
		return "", fmt.Errorf("open workdir: %w", err)
	}
	defer root.Close()
	for range 100 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("generate temporary directory name: %w", err)
		}
		name := prefix + hex.EncodeToString(random[:])
		if err := root.Mkdir(name, 0700); err == nil {
			return name, nil
		} else if !os.IsExist(err) {
			return "", fmt.Errorf("create temporary directory beneath workdir: %w", err)
		}
	}
	return "", fmt.Errorf("create unique temporary directory beneath workdir")
}

// Rename moves a path within workdir through one os.Root, preventing either
// parent path from being redirected outside the root during the operation.
func Rename(workdir, oldPath, newPath string) error {
	for _, path := range []string{oldPath, newPath} {
		rel, err := relative(path)
		if err != nil {
			return err
		}
		if rel == "." {
			return fmt.Errorf("path %q must select a child of workdir", path)
		}
	}
	oldRel, err := rootedRel(workdir, oldPath, false)
	if err != nil {
		return err
	}
	newRel, err := rootedRel(workdir, newPath, false)
	if err != nil {
		return err
	}
	root, err := openCanonicalRoot(workdir)
	if err != nil {
		return fmt.Errorf("open workdir: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(newRel), 0755); err != nil {
		return fmt.Errorf("create rename destination parent: %w", err)
	}
	if err := root.Rename(oldRel, newRel); err != nil {
		return fmt.Errorf("rename %q to %q beneath workdir: %w", oldPath, newPath, err)
	}
	return nil
}

// ValidateTree rejects an existing descendant symlink that resolves outside
// workdir. It is used before invoking external generators that may overwrite
// several files beneath a configured output directory.
func ValidateTree(workdir, path string) error {
	root, err := canonicalRoot(workdir)
	if err != nil {
		return err
	}
	start, err := Resolve(workdir, path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(start); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect tree %q: %w", path, err)
	}
	visited := map[string]bool{}
	var inspect func(string) error
	inspect = func(candidate string) error {
		info, err := os.Lstat(candidate)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			candidate, err = filepath.EvalSymlinks(candidate)
			if err != nil {
				return fmt.Errorf("resolve descendant symlink: %w", err)
			}
			if !within(root, candidate) {
				return fmt.Errorf("descendant symlink %q resolves outside workdir", candidate)
			}
			info, err = os.Stat(candidate)
			if err != nil {
				return err
			}
		}
		if !within(root, candidate) {
			return fmt.Errorf("descendant %q resolves outside workdir", candidate)
		}
		if !info.IsDir() {
			return nil
		}
		canonicalDir, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return err
		}
		if visited[canonicalDir] {
			return nil
		}
		visited[canonicalDir] = true
		entries, err := os.ReadDir(canonicalDir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := inspect(filepath.Join(canonicalDir, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := inspect(start); err != nil {
		return fmt.Errorf("unsafe path beneath %q: %w", path, err)
	}
	return nil
}

func relative(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", fmt.Errorf("path %q must be relative to workdir", path)
	}
	rel := filepath.Clean(path)
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workdir", path)
	}
	return rel, nil
}

func rootedRel(workdir, path string, followFinal bool) (string, error) {
	lexicalRel, err := relative(path)
	if err != nil {
		return "", err
	}
	root, err := canonicalRoot(workdir)
	if err != nil {
		return "", err
	}
	resolved := root
	if lexicalRel != "." {
		if followFinal {
			resolved, err = Resolve(workdir, path)
		} else {
			parent, parentErr := Resolve(workdir, filepath.Dir(lexicalRel))
			if parentErr != nil {
				return "", parentErr
			}
			resolved = filepath.Join(parent, filepath.Base(lexicalRel))
		}
		if err != nil {
			return "", err
		}
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside workdir", path)
	}
	return rel, nil
}

func normalizeWorkdir(workdir string) string {
	if workdir == "" {
		return "."
	}
	return workdir
}

func openCanonicalRoot(workdir string) (*os.Root, error) {
	rootPath, err := canonicalRoot(workdir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	return root, nil
}

func canonicalRoot(workdir string) (string, error) {
	root, err := filepath.Abs(normalizeWorkdir(workdir))
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve workdir symlinks: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat workdir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir %q is not a directory", workdir)
	}
	return filepath.Clean(root), nil
}

func resolveExisting(root, rel, original string) (string, error) {
	current := root
	if rel == "." {
		return current, nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if os.IsNotExist(err) {
			return filepath.Join(append([]string{current}, parts[i:]...)...), nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect path %q: %w", original, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			next, err = filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf("resolve path %q symlinks: %w", original, err)
			}
			if !within(root, next) {
				return "", fmt.Errorf("path %q resolves outside workdir", original)
			}
		}
		current = next
	}
	if !within(root, current) {
		return "", fmt.Errorf("path %q resolves outside workdir", original)
	}
	return filepath.Clean(current), nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
