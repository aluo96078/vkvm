//go:build !windows

package osutils

import "log"

// IsAdmin is a stub for non-Windows platforms
func IsAdmin() bool {
	return false
}

// EnsureFirewallRule is a stub for non-Windows platforms
func EnsureFirewallRule(port int) error {
	log.Println("Firewall: Automatic rule management is only supported on Windows")
	return nil
}
