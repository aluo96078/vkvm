//go:build darwin

package ddc

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"vkvm/internal/embedded"
)

// Stub for Windows on macOS build
func newWindowsController() (Controller, error) {
	return nil, ErrUnsupportedPlatform
}

// macController implements Controller for macOS using m1ddc
type macController struct {
	toolPath string
}

// newMacController creates a new macOS DDC controller
func newMacController() (*macController, error) {
	// Try embedded m1ddc first
	if path, err := embedded.GetToolPath("m1ddc"); err == nil {
		return &macController{toolPath: path}, nil
	}

	// Fallback to system-installed m1ddc
	paths := []string{
		"m1ddc", // In PATH
		"/usr/local/bin/m1ddc",
		"/opt/homebrew/bin/m1ddc",
	}

	for _, p := range paths {
		if path, err := exec.LookPath(p); err == nil {
			return &macController{toolPath: path}, nil
		}
	}

	return nil, ErrToolNotFound
}

// ListMonitors returns all connected monitors
func (c *macController) ListMonitors() ([]Monitor, error) {
	cmd := exec.Command(c.toolPath, "display", "list")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}

	monitors, err := c.parseMonitorList(string(output))
	if err != nil {
		return monitors, err
	}

	// Test DDC support and get current input for each monitor
	for i := range monitors {
		monitors[i].DDCSupported = c.TestDDCSupport(monitors[i].ID)

		// Always try to get current input source (even if DDC test failed)
		// Some KVM switches may support reading but not writing
		if currentInput, err := c.GetCurrentInput(monitors[i].ID); err == nil {
			monitors[i].InputSource = currentInput
			// If we can read input, mark as DDC supported
			if !monitors[i].DDCSupported {
				monitors[i].DDCSupported = true
			}
		}
	}

	return monitors, nil
}

// parseMonitorList parses m1ddc display list output
// Format: [1] VG27AQL3A (776236CB-E781-416A-B419-7A65A34093C1)
func (c *macController) parseMonitorList(output string) ([]Monitor, error) {
	var monitors []Monitor
	scanner := bufio.NewScanner(strings.NewReader(output))

	// Regex to match display entries like: "[1] VG27AQL3A (UUID)" or "[1] (null) (UUID)"
	displayRegex := regexp.MustCompile(`^\[(\d+)\]\s+(.+?)\s+\(([^)]+)\)$`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if matches := displayRegex.FindStringSubmatch(line); matches != nil {
			name := strings.TrimSpace(matches[2])
			// Skip "(null)" entries (usually internal display)
			if name == "(null)" {
				continue
			}
			monitors = append(monitors, Monitor{
				ID:     matches[3], // Use UUID as ID for more reliable addressing
				Name:   name,
				Serial: matches[3], // Keep Serial as UUID too
			})
		}
	}

	return monitors, scanner.Err()
}

// GetCurrentInput gets the current input source for a monitor
func (c *macController) GetCurrentInput(monitorID string) (InputSource, error) {
	// Use 'm1ddc display <id> get input' syntax which is more robust
	cmd := exec.Command(c.toolPath, "display", monitorID, "get", "input")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}

	// Parse the output value
	valueStr := strings.TrimSpace(string(output))
	value, err := strconv.ParseInt(valueStr, 0, 32)
	if err != nil {
		return 0, fmt.Errorf("failed to parse input value: %v", err)
	}

	return InputSource(value), nil
}

// SetInputSource switches a monitor to the specified input
func (c *macController) SetInputSource(monitorID string, source InputSource) error {
	// Use 'm1ddc display <id> set input <val>' syntax
	cmd := exec.Command(c.toolPath, "display", monitorID, "set", "input", fmt.Sprintf("%d", source))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}
	return nil
}

// SetPower sets the monitor power state
func (c *macController) SetPower(monitorID string, on bool) error {
	// Try using 'power' keyword first if supported, or VCP D6
	// m1ddc usually supports 'set power on/off' or 'set D6 <val>'
	// We'll use D6 for consistency with Windows implementation (Standard VCP)
	// 1 = On, 4 = Off/Standby
	val := "4"
	if on {
		val = "1"
	}
	// Syntax: m1ddc display <id> set D6 <val>
	cmd := exec.Command(c.toolPath, "display", monitorID, "set", "D6", val)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}
	return nil
}

// TestDDCSupport tests if a monitor supports DDC/CI by trying to read input source
func (c *macController) TestDDCSupport(monitorID string) bool {
	_, err := c.GetCurrentInput(monitorID)
	return err == nil
}
