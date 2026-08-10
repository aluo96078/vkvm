//go:build !darwin && !windows

package notify

import "log"

func showNotification(title, message string) {
	log.Printf("Notification: %s — %s", title, message)
}
