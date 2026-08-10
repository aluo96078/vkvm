//go:build windows

package service

func companionIPCDescriptor() IPCDescriptor {
	// The pipe name is stable across the SCM (often LocalSystem) and the
	// interactive user session. Authentication is still enforced by the
	// per-process HMAC secret.
	return IPCDescriptor{Network: "namedpipe", Address: `\\.\pipe\vkvm-companion`}
}
