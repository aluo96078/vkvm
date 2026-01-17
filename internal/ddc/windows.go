//go:build windows

package ddc

import (
	"bufio"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"vkvm/internal/embedded"
)

// decodeUTF16 converts UTF-16LE (potentially with BOM) to UTF-8 string
func decodeUTF16(b []byte) string {
	if len(b) < 2 {
		return string(b)
	}

	// First, check if it's valid UTF-8 and doesn't look like UTF-16
	// (UTF-16 usually has many null bytes for ASCII text)
	if utf8.Valid(b) {
		nulls := 0
		for _, v := range b {
			if v == 0 {
				nulls++
			}
		}
		// If less than 10% nulls, probably UTF-8
		if nulls < len(b)/10 {
			return string(b)
		}
	}

	// Check for BOM (EF BB BF for UTF-8 or FF FE for UTF-16LE)
	if b[0] == 0xFF && b[1] == 0xFE {
		// UTF-16LE
		b = b[2:]
	} else if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		// Already UTF-8 with BOM
		return string(b[3:])
	}

	u16 := make([]uint16, len(b)/2)
	for i := 0; i < len(u16); i++ {
		u16[i] = uint16(b[i*2]) | uint16(b[i*2+1])<<8
	}
	return string(utf16.Decode(u16))
}

// Stub for macOS on Windows build
func newMacController() (Controller, error) {
	return nil, ErrUnsupportedPlatform
}

// windowsController implements Controller for Windows using ControlMyMonitor
type windowsController struct {
	toolPath string
}

// newWindowsController creates a new Windows DDC controller
func newWindowsController() (*windowsController, error) {
	// Try embedded ControlMyMonitor first
	if path, err := embedded.GetToolPath("ControlMyMonitor.exe"); err == nil {
		return &windowsController{toolPath: path}, nil
	}

	// Fallback to system-installed ControlMyMonitor
	paths := []string{
		"ControlMyMonitor.exe", // In PATH
		`C:\Program Files\ControlMyMonitor\ControlMyMonitor.exe`,
		`C:\Program Files (x86)\ControlMyMonitor\ControlMyMonitor.exe`,
	}

	for _, p := range paths {
		if path, err := exec.LookPath(p); err == nil {
			return &windowsController{toolPath: path}, nil
		}
	}

	return nil, ErrToolNotFound
}

// ListMonitors returns all connected monitors
func (c *windowsController) ListMonitors() ([]Monitor, error) {
	cmd := exec.Command(c.toolPath, "/smonitors")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}

	// Decode potential UTF-16LE output
	decodedOutput := decodeUTF16(output)

	monitors, err := c.parseMonitorList(decodedOutput)
	if err != nil {
		return monitors, err
	}

	// Test DDC support and get current input for each monitor
	for i := range monitors {
		monitors[i].DDCSupported = c.TestDDCSupport(monitors[i].ID)

		if currentInput, err := c.GetCurrentInput(monitors[i].ID); err == nil {
			monitors[i].InputSource = currentInput
			if !monitors[i].DDCSupported {
				monitors[i].DDCSupported = true
			}
		}
	}

	return monitors, nil
}

// parseMonitorList parses ControlMyMonitor monitor list output
func (c *windowsController) parseMonitorList(output string) ([]Monitor, error) {
	fmt.Printf("DEBUG: Parsing Windows monitor list (%d chars)\n", len(output))
	var monitors []Monitor
	var currentProps map[string]string

	scanner := bufio.NewScanner(strings.NewReader(output))

	// Helper to commit a monitor from accumulated properties
	commit := func() {
		if currentProps == nil {
			return
		}

		// Priority of IDs for ControlMyMonitor:
		// 1. Monitor ID (MONITOR\...) - Highly reliable hardware identifier
		// 2. Device Name (\\.\DISPLAY1\Monitor0) - Reliable OS path
		// 3. Monitor Name (Model name) - Least reliable

		id := currentProps["Monitor ID"]
		if id == "" {
			id = currentProps["Device Name"]
		}
		if id == "" {
			id = currentProps["Monitor Name"]
		}

		if id != "" {
			name := currentProps["Monitor Name"]
			if name == "" {
				name = "Unknown Monitor"
			}

			monitors = append(monitors, Monitor{
				ID:     id,
				Name:   name,
				Serial: currentProps["Serial Number"],
			})
			fmt.Printf("DEBUG: Detected monitor: %s (ID: %s)\n", name, id)
		}
		currentProps = nil
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			commit()
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}

		key := strings.Map(func(r rune) rune {
			if r < 32 || r > 126 {
				return -1
			}
			return r
		}, parts[0])
		key = strings.TrimSpace(key)
		val := strings.Trim(strings.TrimSpace(parts[1]), "\"")

		if currentProps == nil {
			currentProps = make(map[string]string)
		}

		// Use partial matching for keys to be resilient across tool versions
		if strings.Contains(key, "Device Name") {
			currentProps["Device Name"] = val
		} else if strings.Contains(key, "Monitor Name") {
			currentProps["Monitor Name"] = val
		} else if strings.Contains(key, "Serial Number") {
			currentProps["Serial Number"] = val
		} else if strings.Contains(key, "Monitor ID") {
			currentProps["Monitor ID"] = val
		}
	}
	commit() // Don't forget the last one

	fmt.Printf("DEBUG: Found %d monitors total\n", len(monitors))
	return monitors, scanner.Err()
}

// GetCurrentInput gets the current input source for a monitor
func (c *windowsController) GetCurrentInput(monitorID string) (InputSource, error) {
	// VCP code 0x60 is the standard input select code
	cmd := exec.Command(c.toolPath, "/GetValue", monitorID, "60")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}

	// Parse the output value
	valueRegex := regexp.MustCompile(`Current Value:\s*(\d+)`)
	matches := valueRegex.FindStringSubmatch(string(output))
	if len(matches) < 2 {
		return 0, fmt.Errorf("failed to parse input value from output")
	}

	value, err := strconv.ParseInt(matches[1], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("failed to parse input value: %v", err)
	}

	return InputSource(value), nil
}

// SetInputSource switches a monitor to the specified input
func (c *windowsController) SetInputSource(monitorID string, source InputSource) error {
	// VCP code 0x60 is the standard input select code
	args := []string{"/SetValue", monitorID, "60", fmt.Sprintf("%d", source)}

	// Use %q to see exactly what characters are in the ID string (escapes shown)
	log.Printf("DDC: Executing %s with ID %q and input %d", c.toolPath, monitorID, source)

	cmd := exec.Command(c.toolPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("DDC: ControlMyMonitor failed for ID %q. Output: %s", monitorID, string(output))
		return fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}
	return nil
}

// TestDDCSupport tests if a monitor supports DDC/CI by trying multiple VCP codes
func (c *windowsController) TestDDCSupport(monitorID string) bool {
	// Try reading input source (0x60) first
	if _, err := c.GetCurrentInput(monitorID); err == nil {
		return true
	}

	// Fallback: Try reading brightness (0x10) - a very common VCP code
	cmd := exec.Command(c.toolPath, "/GetValue", monitorID, "10")
	if err := cmd.Run(); err == nil {
		return true
	}

	return false
}
