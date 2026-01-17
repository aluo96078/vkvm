//go:build !windows && !darwin

package hotkey

import "log"

func (m *Manager) startPlatform() error {
	log.Println("Hotkey Engine: Global hooks not supported on this platform.")
	return nil
}
