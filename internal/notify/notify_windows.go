//go:build windows

package notify

import (
	"log"
	"os"
	"os/exec"
)

// showNotification displays a non-blocking notification in the interactive
// Windows user session. Values are passed through environment variables so a
// profile name can never become PowerShell source code.
func showNotification(title, message string) {
	const script = `$title = $env:VKVM_NOTIFY_TITLE
$message = $env:VKVM_NOTIFY_MESSAGE
Add-Type -AssemblyName System.Windows.Forms
$notification = New-Object System.Windows.Forms.NotifyIcon
$notification.Icon = [System.Drawing.SystemIcons]::Information
$notification.Visible = $true
$notification.BalloonTipTitle = $title
$notification.BalloonTipText = $message
$notification.ShowBalloonTip(3000)
Start-Sleep -Milliseconds 3500
$notification.Dispose()`

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.Env = append(os.Environ(),
		"VKVM_NOTIFY_TITLE="+title,
		"VKVM_NOTIFY_MESSAGE="+message,
	)
	if err := cmd.Start(); err != nil {
		log.Printf("Notification: failed to start PowerShell: %v", err)
		return
	}

	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("Notification: PowerShell exited with: %v", err)
		}
	}()
}
