// Package embedded provides embedded binaries for DDC control tools.
// The tools are extracted at runtime to a temporary directory.
package embedded

import (
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

//go:embed tools/*
var toolsFS embed.FS

var (
	extractedDir string
	extractOnce  sync.Once
	extractErr   error
)

// GetToolPath returns the path to an extracted tool binary.
// Tools are extracted once on first call and reused.
func GetToolPath(toolName string) (string, error) {
	extractOnce.Do(func() {
		extractedDir, extractErr = extractTools()
	})

	if extractErr != nil {
		return "", extractErr
	}

	toolPath := filepath.Join(extractedDir, toolName)
	if runtime.GOOS == "windows" && filepath.Ext(toolName) == "" {
		toolPath += ".exe"
	}

	if _, err := os.Stat(toolPath); os.IsNotExist(err) {
		return "", fmt.Errorf("tool not found: %s", toolName)
	}

	return toolPath, nil
}

// extractTools extracts all embedded tools to a temporary directory
func extractTools() (string, error) {
	// Create a cache directory that persists across runs
	cacheDir, err := getCacheDir()
	if err != nil {
		return "", err
	}

	// Check if tools already exist
	toolsDir := filepath.Join(cacheDir, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		return "", err
	}

	// Extract tools
	entries, err := toolsFS.ReadDir("tools")
	if err != nil {
		return "", fmt.Errorf("failed to read embedded tools: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		srcPath := "tools/" + entry.Name()
		dstPath := filepath.Join(toolsDir, entry.Name())

		// Skip if already extracted (based on size match)
		srcInfo, _ := toolsFS.Open(srcPath)
		if srcInfo != nil {
			srcStat, _ := srcInfo.Stat()
			if dstStat, err := os.Stat(dstPath); err == nil {
				if srcStat != nil && dstStat.Size() == srcStat.Size() {
					srcInfo.Close()
					continue
				}
			}
			srcInfo.Close()
		}

		// Extract the tool
		if err := extractFile(srcPath, dstPath); err != nil {
			return "", fmt.Errorf("failed to extract %s: %w", entry.Name(), err)
		}
	}

	return toolsDir, nil
}

// extractFile extracts a single file from the embedded FS
func extractFile(srcPath, dstPath string) error {
	src, err := toolsFS.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

// getCacheDir returns the cache directory for extracted tools
func getCacheDir() (string, error) {
	var cacheDir string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cacheDir = filepath.Join(home, "Library", "Caches", "vkvm")
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		cacheDir = filepath.Join(localAppData, "vkvm", "cache")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cacheDir = filepath.Join(home, ".cache", "vkvm")
	}

	return cacheDir, nil
}

// Cleanup removes extracted tools (call on application exit if needed)
func Cleanup() error {
	if extractedDir == "" {
		return nil
	}
	return os.RemoveAll(extractedDir)
}
