// Package hotkey provides global system-wide hotkey and mouse button monitoring.
package hotkey

import (
	"log"
	"strings"
	"sync"
)

// Manager handles global hotkey and mouse button registration and matching
type Manager struct {
	mu           sync.RWMutex
	hotkeys      []*registeredHotkey
	currentState map[string]bool // map of current keys/buttons pressed
}

type registeredHotkey struct {
	parts    []string // e.g., ["CTRL", "ALT", "MOUSE4"]
	original string
	callback func()
}

// NewManager creates a new hotkey manager
func NewManager() *Manager {
	return &Manager{
		currentState: make(map[string]bool),
	}
}

// Register registers a hotkey string (e.g. "Ctrl+Alt+1", "Mouse2+Mouse3") and a callback.
func (m *Manager) Register(hotkeyStr string, callback func()) (int, error) {
	if hotkeyStr == "" {
		return 0, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	parts := strings.Split(strings.ToUpper(hotkeyStr), "+")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}

	m.hotkeys = append(m.hotkeys, &registeredHotkey{
		parts:    parts,
		original: hotkeyStr,
		callback: callback,
	})

	return len(m.hotkeys) - 1, nil
}

// Clear removes all registered hotkeys
func (m *Manager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hotkeys = nil
}

// UpdateState updates the internal state of a key or button and checks for matches.
func (m *Manager) UpdateState(key string, isDown bool) {
	m.mu.Lock()
	key = strings.ToUpper(key)
	if isDown {
		m.currentState[key] = true
	} else {
		delete(m.currentState, key)
	}
	m.mu.Unlock()

	if isDown {
		m.checkMatches()
	}
}

func (m *Manager) checkMatches() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, hk := range m.hotkeys {
		match := true
		// All parts of the hotkey must be in currentState
		for _, part := range hk.parts {
			if !m.currentState[part] {
				match = false
				break
			}
		}

		if match {
			// Basic match found, trigger callback in a goroutine
			log.Printf("Hotkey triggered: %s", hk.original)
			go hk.callback()
		}
	}
}

// Start initiates the platform-specific global hooks.
// This is implemented in platform-specific files (hotkey_windows.go, hotkey_darwin.go).
func (m *Manager) Start() error {
	return m.startPlatform()
}
