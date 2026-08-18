//go:build darwin

package autostart

import (
	"strings"
	"testing"
)

func TestRenderLaunchAgentPlistKeepsMarkupAndEscapesValues(t *testing.T) {
	rendered, err := renderLaunchAgentPlist("/Users/test&name/VKVM.app/Contents/MacOS/vkvm", "/Users/test&name/Library/Logs/VKVM/agent.log")
	if err != nil {
		t.Fatalf("render LaunchAgent plist: %v", err)
	}
	plist := string(rendered)
	if !strings.HasPrefix(plist, "<?xml version=") {
		t.Fatalf("plist markup was escaped: %q", plist[:min(len(plist), 32)])
	}
	if strings.Contains(plist, "&lt;") || strings.Contains(plist, "&gt;") {
		t.Fatalf("plist markup contains HTML entities: %s", plist)
	}
	if strings.Contains(plist, "--companion") {
		t.Fatalf("macOS LaunchAgent must start the interactive process, not the Windows companion mode: %s", plist)
	}
	if !strings.Contains(plist, "/Users/test&amp;name/VKVM.app/Contents/MacOS/vkvm") {
		t.Fatalf("executable path was not XML escaped: %s", plist)
	}
	if !strings.Contains(plist, "/Users/test&amp;name/Library/Logs/VKVM/agent.log") {
		t.Fatalf("log path was not XML escaped: %s", plist)
	}
}
