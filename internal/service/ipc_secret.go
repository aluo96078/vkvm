package service

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ipcSecretSize = 32

// IPCSecretPath is the owner-scoped hand-off file used by a login companion
// to authenticate to the core. The file is deliberately separate from the
// socket/pipe descriptor so diagnostics never expose the credential itself.
func IPCSecretPath() string {
	if override := os.Getenv("VKVM_IPC_SECRET_PATH"); override != "" {
		return override
	}
	descriptor := CompanionIPCDescriptor()
	if descriptor.Network == "unix" && descriptor.Address != "" {
		return descriptor.Address + ".secret"
	}
	if descriptor.Network == "namedpipe" {
		if programData := os.Getenv("ProgramData"); programData != "" {
			return filepath.Join(programData, "VKVM", "companion.secret")
		}
	}
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		configDir = os.TempDir()
	}
	return filepath.Join(configDir, "VKVM", "companion.secret")
}

// ProvisionIPCSecret atomically publishes a fresh process credential with
// owner-only permissions. A companion should read it only after the local
// service endpoint has been discovered, and the core removes it on shutdown.
func ProvisionIPCSecret(secret []byte) error {
	if len(secret) != ipcSecretSize {
		return fmt.Errorf("IPC secret must be %d bytes", ipcSecretSize)
	}
	path := IPCSecretPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create IPC secret directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".companion.secret.*")
	if err != nil {
		return fmt.Errorf("create IPC secret temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict IPC secret permissions: %w", err)
	}
	if _, err := tmp.WriteString(hex.EncodeToString(secret)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write IPC secret: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flush IPC secret: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close IPC secret: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish IPC secret: %w", err)
	}
	if err := restrictIPCSecretFile(path); err != nil {
		return err
	}
	return nil
}

// ReadIPCSecret reads and validates the currently provisioned credential.
func ReadIPCSecret() ([]byte, error) {
	data, err := os.ReadFile(IPCSecretPath())
	if err != nil {
		return nil, err
	}
	encoded := strings.TrimSpace(string(data))
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != ipcSecretSize {
		return nil, errors.New("invalid IPC secret")
	}
	// Force a constant-time length check before returning a copy. This keeps
	// malformed/truncated files from becoming valid credentials in callers.
	if subtle.ConstantTimeCompare(decoded, make([]byte, ipcSecretSize)) == 1 {
		return nil, errors.New("IPC secret is all zeroes")
	}
	return append([]byte(nil), decoded...), nil
}

// RemoveIPCSecret invalidates the credential published by the core. It is
// idempotent so shutdown paths can call it more than once.
func RemoveIPCSecret() error {
	err := os.Remove(IPCSecretPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
