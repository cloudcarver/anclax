package tools

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func CopyToInitFiles(src string, dst string, excluded []string) (err error) {
	srcRoot, err := openDirectoryRoot(src, false)
	if err != nil {
		return fmt.Errorf("failed to open source directory: %w", err)
	}
	defer closeTemplateResource(&err, srcRoot, "source directory")

	dstRoot, err := openDirectoryRoot(dst, true)
	if err != nil {
		return fmt.Errorf("failed to open destination directory: %w", err)
	}
	defer closeTemplateResource(&err, dstRoot, "destination directory")

	generated := map[string]bool{".": true}
	err = fs.WalkDir(srcRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		relPath := filepath.FromSlash(path)
		for _, pattern := range excluded {
			matched, err := filepath.Match(pattern, relPath)
			if err != nil {
				return fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
			}
			if matched {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}

		info, err := srcRoot.Lstat(relPath)
		if err != nil {
			return fmt.Errorf("failed to inspect source path %s: %w", relPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %s is a symbolic link", relPath)
		}
		if info.IsDir() {
			directory, err := openDirectoryBelow(dstRoot, relPath, true)
			if err != nil {
				return fmt.Errorf("failed to prepare destination directory %s: %w", relPath, err)
			}
			if err := directory.Close(); err != nil {
				return fmt.Errorf("failed to close destination directory %s: %w", relPath, err)
			}
			generated[relPath] = true
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
		generated[destPath] = true
		return nil
	})
	if err != nil {
		return err
	}

	// Remove stale entries through the same rooted handles used for copying.
	return fs.WalkDir(dstRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) (err error) {
		if walkErr != nil {
			return walkErr
		}
		relPath := filepath.FromSlash(path)
		if generated[relPath] {
			return nil
		}
		parent, err := openDirectoryBelow(dstRoot, filepath.Dir(relPath), false)
		if err != nil {
			return fmt.Errorf("failed to open stale template parent %s: %w", relPath, err)
		}
		defer closeTemplateResource(&err, parent, "stale template parent")
		if err := parent.RemoveAll(filepath.Base(relPath)); err != nil {
			return fmt.Errorf("failed to remove stale template %s: %w", relPath, err)
		}
		if entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
}

// Paths inside the working directory stay pinned to it, including their parent
// components. Explicit roots elsewhere may have aliases above them (such as
// macOS /tmp); their roots and contents still reject symlinks.
func openDirectoryRoot(path string, create bool) (*os.Root, error) {
	if filepath.IsLocal(path) {
		root, err := os.OpenRoot(".")
		if err != nil {
			return nil, err
		}
		return walkTemplateDirectories(root, path, create)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path %s: %w", path, err)
	}
	workingRoot, err := os.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	workingInfo, err := workingRoot.Stat(".")
	if err != nil {
		return nil, errors.Join(err, workingRoot.Close())
	}
	rootPath := filepath.VolumeName(absPath) + string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(absPath, rootPath), string(filepath.Separator))
	prefix := rootPath
	// Match directory identity, so an absolute path through /var or /tmp still
	// gets the same working-directory protection as its relative equivalent.
	for i := 0; i < len(components)-1; i++ {
		prefix = filepath.Join(prefix, components[i])
		info, statErr := os.Stat(prefix)
		if os.IsNotExist(statErr) {
			break
		}
		if statErr != nil {
			return nil, errors.Join(statErr, workingRoot.Close())
		}
		if os.SameFile(workingInfo, info) {
			return walkTemplateDirectories(workingRoot, filepath.Join(components[i+1:]...), create)
		}
	}
	if err := workingRoot.Close(); err != nil {
		return nil, err
	}
	parent := filepath.Dir(absPath)
	tail := []string{filepath.Base(absPath)}
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			absPath = resolved
			for i := len(tail) - 1; i >= 0; i-- {
				absPath = filepath.Join(absPath, tail[i])
			}
			break
		}
		if !create || !os.IsNotExist(err) || parent == filepath.Dir(parent) {
			return nil, fmt.Errorf("failed to resolve parent of %s: %w", path, err)
		}
		tail = append(tail, filepath.Base(parent))
		parent = filepath.Dir(parent)
	}

	rootPath = filepath.VolumeName(absPath) + string(filepath.Separator)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open filesystem root %s: %w", rootPath, err)
	}
	relPath, err := filepath.Rel(rootPath, absPath)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return walkTemplateDirectories(root, relPath, create)
}

func openDirectoryBelow(root *os.Root, path string, create bool) (*os.Root, error) {
	ownedRoot, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	return walkTemplateDirectories(ownedRoot, path, create)
}

// walkTemplateDirectories owns root and returns an owned handle to the final
// directory. Each parent stays pinned until its child has been verified.
func walkTemplateDirectories(root *os.Root, path string, create bool) (*os.Root, error) {
	path = filepath.Clean(path)
	if !filepath.IsLocal(path) {
		return nil, errors.Join(fmt.Errorf("directory path %s escapes its root", path), root.Close())
	}
	if path == "." {
		return root, nil
	}
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		nextRoot, openErr := openTemplateDirectory(root, component, create)
		closeErr := root.Close()
		if openErr != nil {
			return nil, errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			return nil, errors.Join(fmt.Errorf("failed to close parent directory: %w", closeErr), nextRoot.Close())
		}
		root = nextRoot
	}
	return root, nil
}

func openTemplateDirectory(root *os.Root, name string, create bool) (*os.Root, error) {
	info, err := root.Lstat(name)
	if err != nil && os.IsNotExist(err) && create {
		if err := root.Mkdir(name, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", name, err)
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to inspect directory %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("directory path %s is a symbolic link", name)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("directory path %s is not a directory", name)
	}
	opened, err := root.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("failed to open directory %s: %w", name, err)
	}
	openedInfo, err := opened.Stat(".")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("failed to inspect opened directory %s: %w", name, err), opened.Close())
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.Join(fmt.Errorf("directory path %s changed while opening", name), opened.Close())
	}
	return opened, nil
}

func copyRegularFile(srcRoot, dstRoot *os.Root, srcPath, dstPath string, expectedInfo os.FileInfo) (err error) {
	srcParent, err := openDirectoryBelow(srcRoot, filepath.Dir(srcPath), false)
	if err != nil {
		return err
	}
	defer closeTemplateResource(&err, srcParent, "source parent")
	srcFile, err := srcParent.Open(filepath.Base(srcPath))
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer closeTemplateResource(&err, srcFile, "source file")
	openedInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to inspect opened source file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(expectedInfo, openedInfo) {
		return fmt.Errorf("source path changed while opening")
	}

	dstParent, err := openDirectoryBelow(dstRoot, filepath.Dir(dstPath), false)
	if err != nil {
		return err
	}
	defer closeTemplateResource(&err, dstParent, "destination parent")
	name := filepath.Base(dstPath)
	perm := os.FileMode(0666)
	destInfo, statErr := dstParent.Lstat(name)
	existed := statErr == nil
	if existed {
		if destInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path %s is a symbolic link", dstPath)
		}
		if !destInfo.Mode().IsRegular() {
			return fmt.Errorf("destination path %s is not a regular file", dstPath)
		}
		perm = destInfo.Mode().Perm()
		// Exclusive recreation avoids following a replaced symlink or writing
		// through an existing hard link to a file outside the destination tree.
		if err := dstParent.Remove(name); err != nil {
			return fmt.Errorf("failed to remove existing destination file: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to inspect destination file: %w", statErr)
	}
	destFile, err := dstParent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer closeTemplateResource(&err, destFile, "destination file")
	if existed {
		if err := destFile.Chmod(perm); err != nil {
			return fmt.Errorf("failed to preserve destination permissions: %w", err)
		}
	}
	if _, err := io.Copy(destFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}
	return nil
}

func closeTemplateResource(result *error, resource io.Closer, name string) {
	if err := resource.Close(); err != nil {
		*result = errors.Join(*result, fmt.Errorf("failed to close %s: %w", name, err))
	}
}
