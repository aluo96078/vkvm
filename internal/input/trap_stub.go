//go:build !windows

package input

import (
	"fmt"
)

// Stub implementation for non-Windows platforms

// Trap represents a stub input trap
type Trap struct{}

// NewTrap creates a new stub trap
func NewTrap() *Trap {
	return &Trap{}
}

// Start begins capturing input (stub)
func (t *Trap) Start() error {
	return fmt.Errorf("input trapping not supported on this platform")
}

// Stop stops capturing input (stub)
func (t *Trap) Stop() error {
	return nil
}

// Events returns the input event channel (stub)
func (t *Trap) Events() <-chan InputEvent {
	return nil
}

// SetKillSwitch registers kill switch (stub)
func (t *Trap) SetKillSwitch(callback func()) error {
	return fmt.Errorf("kill switch not supported on this platform")
}
