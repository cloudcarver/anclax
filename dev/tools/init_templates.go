package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func CopyToInitFiles(src string, dst string, excluded []string) error {
	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Walk through the source directory recursively
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
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
			if strings.Contains(relPath, excludePattern) {
				return nil
			}
		}

		// Destination path
		destPath := filepath.Join(dst, relPath)

		// Handle directories
		if info.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		// Handle files
		// Add .embed suffix to .go and go.mod files
		if strings.HasSuffix(relPath, ".go") || relPath == "go.mod" {
			destPath = destPath + ".embed"
		}

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
}
