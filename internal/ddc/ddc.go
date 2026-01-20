// Package ddc provides DDC/CI control abstraction for monitor input switching.
package ddc

import "runtime"

// InputSource represents monitor input sources
type InputSource int

const (
	InputSourceDP1   InputSource = 0x0F // DisplayPort 1
	InputSourceDP2   InputSource = 0x10 // DisplayPort 2
	InputSourceHDMI1 InputSource = 0x11 // HDMI 1
	InputSourceHDMI2 InputSource = 0x12 // HDMI 2
	InputSourceUSBC  InputSource = 0x1B // USB-C
)

// Monitor represents a connected display
type Monitor struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	DeviceName   string      `json:"device_name,omitempty"`
	Serial       string      `json:"serial,omitempty"`
	InputSource  InputSource `json:"input_source"`
	DDCSupported bool        `json:"ddc_supported"`
}

// Controller defines the interface for DDC control operations
type Controller interface {
	// ListMonitors returns all connected monitors
	ListMonitors() ([]Monitor, error)

	// GetCurrentInput gets the current input source for a monitor
	GetCurrentInput(monitorID string) (InputSource, error)

	// SetInputSource switches a monitor to the specified input
	SetInputSource(monitorID string, source InputSource) error

	// SetPower set the monitor power state (true: On, false: Off/Standby)
	SetPower(monitorID string, on bool) error

	// TestDDCSupport tests if a monitor supports DDC/CI
	TestDDCSupport(monitorID string) bool
}

// NewController creates a platform-specific DDC controller
func NewController() (Controller, error) {
	switch runtime.GOOS {
	case "darwin":
		return newMacController()
	case "windows":
		return newWindowsController()
	default:
		return nil, ErrUnsupportedPlatform
	}
}

// InputSourceName returns a human-readable name for the input source
func (s InputSource) String() string {
	switch s {
	case InputSourceDP1:
		return "DisplayPort 1"
	case InputSourceDP2:
		return "DisplayPort 2"
	case InputSourceHDMI1:
		return "HDMI 1"
	case InputSourceHDMI2:
		return "HDMI 2"
	case InputSourceUSBC:
		return "USB-C"
	default:
		return "Unknown"
	}
}
