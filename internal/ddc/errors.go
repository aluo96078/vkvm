package ddc

import "errors"

var (
	// ErrUnsupportedPlatform is returned when running on an unsupported OS
	ErrUnsupportedPlatform = errors.New("unsupported platform")

	// ErrMonitorNotFound is returned when the specified monitor cannot be found
	ErrMonitorNotFound = errors.New("monitor not found")

	// ErrDDCNotSupported is returned when DDC/CI is not supported by the monitor
	ErrDDCNotSupported = errors.New("DDC/CI not supported by monitor")

	// ErrToolNotFound is returned when the required external tool is not found
	ErrToolNotFound = errors.New("required tool not found")

	// ErrCommandFailed is returned when the external command fails
	ErrCommandFailed = errors.New("command execution failed")
)
