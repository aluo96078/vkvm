//go:build !darwin && !windows

package osutils

import "log"

// WakeUp is a no-op stub for unsupported platforms
func WakeUp() {
	log.Println("WakeUp: Not implemented on this platform")
}
