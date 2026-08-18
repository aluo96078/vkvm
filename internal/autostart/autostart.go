// Package autostart provides platform-specific auto-start registration.
package autostart

// Enable enables the interactive user-session process to start at login.
func Enable() error { return enablePlatform() }

// Disable removes the interactive user-session auto-start registration.
func Disable() error { return disablePlatform() }

// IsEnabled reports whether the user-session auto-start registration exists.
func IsEnabled() bool { return isEnabledPlatform() }
