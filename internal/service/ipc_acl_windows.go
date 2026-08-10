//go:build windows

package service

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Interactive Users can open the pipe/file, while HMAC authentication still
// prevents arbitrary same-machine callers from issuing RPCs. LocalSystem and
// Administrators are retained for SCM diagnostics and recovery.
const companionIPCSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;IU)"

func companionIPCSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	return windows.SecurityDescriptorFromString(companionIPCSDDL)
}

func companionIPCSecurityAttributes() (*windows.SecurityAttributes, error) {
	sd, err := companionIPCSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}, nil
}

func restrictIPCSecretFile(path string) error {
	sd, err := companionIPCSecurityDescriptor()
	if err != nil {
		return fmt.Errorf("build IPC ACL: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read IPC ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("apply IPC ACL: %w", err)
	}
	return nil
}
