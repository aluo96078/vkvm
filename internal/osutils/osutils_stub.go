//go:build !windows

package osutils

import (
	"fmt"
	"log"
	"os/exec"
	"runtime"
)

// IsAdmin is a stub for non-Windows platforms
func IsAdmin() bool {
	return false
}

// TurnOffDisplay puts the monitor to sleep
func TurnOffDisplay() error {
	if runtime.GOOS == "darwin" {
		return exec.Command("pmset", "displaysleepnow").Run()
	}
	return fmt.Errorf("TurnOffDisplay not supported on %s", runtime.GOOS)
}

// EnsureFirewallRule is a stub for non-Windows platforms
func EnsureFirewallRule(port int) error {
	log.Println("Firewall: Automatic rule management is only supported on Windows")
	return nil
}
