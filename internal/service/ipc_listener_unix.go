//go:build !windows

package service

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type cleanupUnixListener struct {
	net.Listener
	path string
}

func (l *cleanupUnixListener) Close() error {
	err := l.Listener.Close()
	removeErr := os.Remove(l.path)
	if removeErr != nil && !os.IsNotExist(removeErr) && err == nil {
		err = removeErr
	}
	return err
}

func listenCompanionIPC() (net.Listener, error) {
	descriptor := CompanionIPCDescriptor()
	if descriptor.Network != "unix" || descriptor.Address == "" {
		return nil, fmt.Errorf("unsupported local IPC descriptor: %+v", descriptor)
	}
	if err := os.MkdirAll(filepath.Dir(descriptor.Address), 0700); err != nil {
		return nil, err
	}
	// The endpoint is owner-scoped and may be left behind after a crash. It is
	// safe to remove only this exact socket path before binding a new listener.
	_ = os.Remove(descriptor.Address)
	listener, err := net.Listen("unix", descriptor.Address)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(descriptor.Address, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(descriptor.Address)
		return nil, err
	}
	return &cleanupUnixListener{Listener: listener, path: descriptor.Address}, nil
}
