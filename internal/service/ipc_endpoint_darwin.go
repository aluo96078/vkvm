//go:build darwin

package service

import (
	"os"
	"path/filepath"
)

func companionIPCDescriptor() IPCDescriptor {
	home, err := os.UserHomeDir()
	if err != nil {
		return IPCDescriptor{Network: "unix", Address: filepath.Join(os.TempDir(), "vkvm-companion.sock")}
	}
	dir := filepath.Join(home, "Library", "Application Support", "vkvm")
	return IPCDescriptor{Network: "unix", Address: filepath.Join(dir, "companion.sock")}
}
