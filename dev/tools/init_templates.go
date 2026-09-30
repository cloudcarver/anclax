package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func CopyToInitFiles(src string, dst string, excluded []string) error {
	dst = filepath.Clean(dst)
	generated := map[string]bool{dst: true}
	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Walk through the source directory recursively
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from source directory
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Skip the source directory itself
		if relPath == "." {
			return nil
		}

		// Check if path should be excluded
		for _, excludePattern := range excluded {
			matched, err := filepath.Match(excludePattern, relPath)
			if err != nil {
				return fmt.Errorf("invalid exclude pattern %q: %w", excludePattern, err)
			}
			if matched {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		// Destination path
		destPath := filepath.Join(dst, relPath)

		// Handle directories
		if info.IsDir() {
			generated[destPath] = true
			return os.MkdirAll(destPath, 0755)
		}

		// Handle files
		// Add .embed suffix to .go and go.mod files
		if strings.HasSuffix(relPath, ".go") || relPath == "go.mod" {
			destPath = destPath + ".embed"
		}
		generated[destPath] = true

		// Copy file content
		srcFile, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open source file %s: %w", path, err)
		}
		defer srcFile.Close()

		destFile, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("failed to create destination file %s: %w", destPath, err)
		}
		defer destFile.Close()

		if _, err = io.Copy(destFile, srcFile); err != nil {
			return fmt.Errorf("failed to copy content from %s to %s: %w", path, destPath, err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Deleted or excluded example files must not survive in new scaffolds.
	return filepath.Walk(dst, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if generated[path] {
			return nil
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("failed to remove stale template %s: %w", path, err)
		}
		if info.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}
