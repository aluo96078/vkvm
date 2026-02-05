//go:build !darwin

package input

import (
	"fmt"
)

// Stub implementation for non-macOS platforms

// Injector represents a stub input injector
type Injector struct{}

// NewInjector creates a new stub injector
func NewInjector() *Injector {
	return &Injector{}
}

// InjectMouseMove injects a mouse movement event (stub)
func (i *Injector) InjectMouseMove(dx, dy int) error {
	return fmt.Errorf("input injection not supported on this platform")
}

// InjectMouseButton injects a mouse button event (stub)
func (i *Injector) InjectMouseButton(button int, pressed bool) error {
	return fmt.Errorf("input injection not supported on this platform")
}

// InjectKey injects a keyboard event (stub)
func (i *Injector) InjectKey(keyCode uint16, pressed bool, modifiers uint16) error {
	return fmt.Errorf("input injection not supported on this platform")
}
