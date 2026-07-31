package tools

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func CopyToInitFiles(src string, dst string, excluded []string) error {
	srcRoot, err := openDirectoryRoot(src, false)
	if err != nil {
		return fmt.Errorf("failed to open source directory: %w", err)
	}
	defer srcRoot.Close()

	dstRoot, err := openDirectoryRoot(dst, true)
	if err != nil {
		return fmt.Errorf("failed to open destination directory: %w", err)
	}
	defer dstRoot.Close()

	return fs.WalkDir(srcRoot.FS(), ".", func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}

		relPath := filepath.FromSlash(path)
		info, err := srcRoot.Lstat(relPath)
		if err != nil {
			return fmt.Errorf("failed to inspect source path %s: %w", relPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %s is a symbolic link", relPath)
		}

		for _, excludePattern := range excluded {
			if strings.Contains(relPath, excludePattern) {
				if info.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}

		if info.IsDir() {
			if err := ensureDestinationDirectories(dstRoot, relPath, true); err != nil {
				return fmt.Errorf("failed to prepare destination directory %s: %w", relPath, err)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source path %s is not a regular file", relPath)
		}

		destPath := relPath
		if strings.HasSuffix(relPath, ".go") || relPath == "go.mod" {
			destPath += ".embed"
		}

		if err := copyRegularFile(srcRoot, dstRoot, relPath, destPath, info); err != nil {
			return fmt.Errorf("failed to copy %s to %s: %w", relPath, destPath, err)
		}
		return nil
	})
}

// openDirectoryRoot walks path one component at a time so no symbolic link is
// followed while opening or creating the directory root.
func openDirectoryRoot(path string, create bool) (*os.Root, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path %s: %w", path, err)
	}

	volume := filepath.VolumeName(absPath)
	rootPath := volume + string(filepath.Separator)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open filesystem root %s: %w", rootPath, err)
	}

	relPath, err := filepath.Rel(rootPath, absPath)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("failed to resolve path relative to %s: %w", rootPath, err)
	}
	if relPath == "." {
		return root, nil
	}

	walkedPath := rootPath
	for _, component := range strings.Split(relPath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}

		walkedPath = filepath.Join(walkedPath, component)
		info, err := root.Lstat(component)
		if err != nil && os.IsNotExist(err) && create {
			if err := root.Mkdir(component, 0755); err != nil {
				root.Close()
				return nil, fmt.Errorf("failed to create directory %s: %w", walkedPath, err)
			}
			info, err = root.Lstat(component)
		}
		if err != nil {
			root.Close()
			return nil, fmt.Errorf("failed to inspect directory %s: %w", walkedPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, fmt.Errorf("directory path %s contains a symbolic link", walkedPath)
		}
		if !info.IsDir() {
			root.Close()
			return nil, fmt.Errorf("directory path component %s is not a directory", walkedPath)
		}

		nextRoot, err := root.OpenRoot(component)
		if err != nil {
			root.Close()
			return nil, fmt.Errorf("failed to open directory %s: %w", walkedPath, err)
		}
		openedInfo, err := nextRoot.Stat(".")
		if err != nil {
			nextRoot.Close()
			root.Close()
			return nil, fmt.Errorf("failed to verify directory %s: %w", walkedPath, err)
		}
		if !os.SameFile(info, openedInfo) {
			nextRoot.Close()
			root.Close()
			return nil, fmt.Errorf("directory path component %s changed while opening", walkedPath)
		}

		if err := root.Close(); err != nil {
			nextRoot.Close()
			return nil, fmt.Errorf("failed to close parent directory for %s: %w", walkedPath, err)
		}
		root = nextRoot
	}

	return root, nil
}

func ensureDestinationDirectories(root *os.Root, path string, create bool) error {
	cleanPath := filepath.Clean(path)
	if cleanPath == "." {
		return nil
	}
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return fmt.Errorf("destination path %s escapes the destination root", path)
	}

	current := ""
	for _, component := range strings.Split(cleanPath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)

		info, err := root.Lstat(current)
		if err != nil && os.IsNotExist(err) && create {
			if err := root.Mkdir(current, 0755); err != nil {
				return fmt.Errorf("failed to create destination directory %s: %w", current, err)
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("failed to inspect destination path component %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path component %s is a symbolic link", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("destination path component %s is not a directory", current)
		}
	}

	return nil
}

func copyRegularFile(srcRoot, dstRoot *os.Root, srcPath, dstPath string, expectedInfo os.FileInfo) error {
	srcFile, err := srcRoot.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	openedInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to inspect opened source file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(expectedInfo, openedInfo) {
		return fmt.Errorf("source path changed while opening")
	}

	parent := filepath.Dir(dstPath)
	if err := ensureDestinationDirectories(dstRoot, parent, false); err != nil {
		return err
	}

	perm := os.FileMode(0666)
	destInfo, err := dstRoot.Lstat(dstPath)
	if err == nil {
		if destInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path %s is a symbolic link", dstPath)
		}
		if !destInfo.Mode().IsRegular() {
			return fmt.Errorf("destination path %s is not a regular file", dstPath)
		}
		perm = destInfo.Mode().Perm()
		// Removing the checked entry and recreating it exclusively prevents the
		// final open from following a symlink installed at this path.
		if err := dstRoot.Remove(dstPath); err != nil {
			return fmt.Errorf("failed to remove existing destination file: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect destination file: %w", err)
	}

	destFile, err := dstRoot.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}

	_, copyErr := io.Copy(destFile, srcFile)
	closeErr := destFile.Close()
	if copyErr != nil {
		return fmt.Errorf("failed to copy file content: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close destination file: %w", closeErr)
	}
	return nil
}
