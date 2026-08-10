//go:build darwin

package autostart

import (
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
        <string>--companion</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>10</integer>
    <key>ProcessType</key>
    <string>Interactive</string>
    <key>StandardOutPath</key>
    <string>{{.LogPath}}</string>
    <key>StandardErrorPath</key>
    <string>{{.LogPath}}</string>
</dict>
</plist>`

func enablePlatform() error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0700); err != nil {
		return err
	}
	logDir := filepath.Join(home, "Library", "Logs", "VKVM")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return err
	}
	plistPath := filepath.Join(launchAgentsDir, "com.vkvm.agent.plist")
	logPath := filepath.Join(logDir, "agent.log")
	tmpl, err := template.New("plist").Parse(macLaunchAgentPlist)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(launchAgentsDir, ".com.vkvm.agent.*.plist")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmpl.Execute(tmp, struct {
		ExecutablePath string
		LogPath        string
	}{execPath, logPath}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, plistPath); err != nil {
		return err
	}

	domain := "gui/" + strconv.Itoa(os.Getuid())
	// Reload is idempotent for the user's own LaunchAgent. bootout may fail
	// when this is the first enable, which is expected.
	_ = exec.Command("launchctl", "bootout", domain, plistPath).Run()
	if output, err := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %w: %s", err, output)
	}
	if output, err := exec.Command("launchctl", "kickstart", "-k", domain+"/com.vkvm.agent").CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart failed: %w: %s", err, output)
	}
	return nil
}

func disablePlatform() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.vkvm.agent.plist")
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, plistPath).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func isEnabledPlatform() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.vkvm.agent.plist")
	_, err = os.Stat(plistPath)
	return err == nil
}
