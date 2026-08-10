//go:build !windows

package service

import "os"

func restrictIPCSecretFile(path string) error {
	return os.Chmod(path, 0600)
}
