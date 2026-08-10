//go:build !darwin && !windows

package service

import "os"

func companionIPCDescriptor() IPCDescriptor {
	return IPCDescriptor{Network: "unix", Address: os.TempDir() + "/vkvm-companion.sock"}
}
