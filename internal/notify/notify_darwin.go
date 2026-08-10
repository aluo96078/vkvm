//go:build darwin

package notify

import (
	"log"
	"os/exec"
)

func showNotification(title, message string) {
	script := `display notification "` + EscapeAppleScriptString(message) + `" with title "` + EscapeAppleScriptString(title) + `"`
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		log.Printf("Notification: failed to show: %v", err)
	}
}
