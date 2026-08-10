// Package notify provides cross-platform system notifications for VKVM.
package notify

import "strings"

// SwitchNotify sends a system notification indicating that control has
// been switched to the given profile.
func SwitchNotify(profileName string) {
	showNotification("VKVM", "控制權已切換至 "+profileName)
}

// EscapeAppleScriptString escapes a value used inside an AppleScript string
// literal. It is kept platform-neutral so it can be unit tested on every
// supported build target.
func EscapeAppleScriptString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\r", `\r`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}
