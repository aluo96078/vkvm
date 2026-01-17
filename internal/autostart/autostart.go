// Package autostart provides auto-start functionality.
package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"text/template"
)

const macLaunchAgentPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.vkvm.agent</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.ExecutablePath}}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
</dict>
</plist>`

// Enable enables auto-start on login
func Enable() error {
	switch runtime.GOOS {
	case "darwin":
		return enableMac()
	case "windows":
		return enableWindows()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// Disable disables auto-start on login
func Disable() error {
	switch runtime.GOOS {
	case "darwin":
		return disableMac()
	case "windows":
		return disableWindows()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// IsEnabled checks if auto-start is enabled
func IsEnabled() bool {
	switch runtime.GOOS {
	case "darwin":
		return isEnabledMac()
	case "windows":
		return isEnabledWindows()
	default:
		return false
	}
}

// macOS implementation
func enableMac() error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		return err
	}

	plistPath := filepath.Join(launchAgentsDir, "com.vkvm.agent.plist")

	tmpl, err := template.New("plist").Parse(macLaunchAgentPlist)
	if err != nil {
		return err
	}

	f, err := os.Create(plistPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, struct{ ExecutablePath string }{execPath})
}

func disableMac() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.vkvm.agent.plist")
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func isEnabledMac() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.vkvm.agent.plist")
	_, err = os.Stat(plistPath)
	return err == nil
}

// Windows implementation (stub - requires golang.org/x/sys/windows/registry)
func enableWindows() error {
	// Note: Full implementation requires registry access
	// For now, provide instructions
	return fmt.Errorf("Windows auto-start not yet implemented. Add executable to shell:startup folder manually")
}

func disableWindows() error {
	return fmt.Errorf("Windows auto-start not yet implemented")
}

func isEnabledWindows() bool {
	return false
}
