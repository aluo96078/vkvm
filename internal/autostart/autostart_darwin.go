//go:build darwin

package autostart

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
        <string>{{xml .ExecutablePath}}</string>
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
    <string>{{xml .LogPath}}</string>
    <key>StandardErrorPath</key>
    <string>{{xml .LogPath}}</string>
</dict>
</plist>`

// renderLaunchAgentPlist renders the LaunchAgent document without escaping the
// XML markup itself. html/template is intentionally not used here: it would
// turn the plist's opening tags into entities such as "&lt;", which launchd
// rejects before it can load the agent. Values are still XML-escaped because a
// user home or executable path may contain an ampersand or other XML syntax.
func renderLaunchAgentPlist(executablePath, logPath string) ([]byte, error) {
	xmlEscape := func(value string) string {
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
			return value
		}
		return escaped.String()
	}
	tmpl, err := template.New("plist").Funcs(template.FuncMap{"xml": xmlEscape}).Parse(macLaunchAgentPlist)
	if err != nil {
		return nil, err
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct {
		ExecutablePath string
		LogPath        string
	}{executablePath, logPath}); err != nil {
		return nil, err
	}
	return rendered.Bytes(), nil
}

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
	tmp, err := os.CreateTemp(launchAgentsDir, ".com.vkvm.agent.*.plist")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	rendered, err := renderLaunchAgentPlist(execPath, logPath)
	if err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(rendered); err != nil {
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
