//go:build windows

package ddc

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
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

// sanitizeForFilename replaces characters not safe for filenames
func sanitizeForFilename(s string) string {
	re := regexp.MustCompile(`[^A-Za-z0-9_.-]`)
	return re.ReplaceAllString(s, "_")
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
	// Try project tools directory first (user may have updated version here)
	paths := []string{
		`D:\vkvm\tools\ControlMyMonitor.exe`, // Project tools directory (priority)
		"ControlMyMonitor.exe",                // In PATH
		`C:\Program Files\ControlMyMonitor\ControlMyMonitor.exe`,
		`C:\Program Files (x86)\ControlMyMonitor\ControlMyMonitor.exe`,
	}

	for _, p := range paths {
		if path, err := exec.LookPath(p); err == nil {
			log.Printf("DDC: Using ControlMyMonitor at %s", path)
			return &windowsController{toolPath: path}, nil
		}
	}

	// Try embedded ControlMyMonitor as last resort
	if path, err := embedded.GetToolPath("ControlMyMonitor.exe"); err == nil {
		log.Printf("DDC: Using embedded ControlMyMonitor at %s", path)
		return &windowsController{toolPath: path}, nil
	}

	log.Printf("DDC: ControlMyMonitor.exe not found in any of the expected paths")
	return nil, ErrToolNotFound
}

// runWithTempFile runs the tool with arguments and captures output from a temporary file.
// outputSwitch is the switch that specifies the output file (e.g., "/smonitors", "/scomma").
func (c *windowsController) runWithTempFile(outputSwitch string, preArgs ...string) ([]byte, error) {
	tmpDir := os.TempDir()
	// Use a subdirectory to ensure we can write to it and it's isolated
	// But os.TempDir() is usually writable.
	// Use nanosecond timestamp to avoid collision
	tmpFile := filepath.Join(tmpDir, fmt.Sprintf("vkvm_ddc_%d.txt", time.Now().UnixNano()))
	
	// Ensure cleanup
	defer func() {
		// Try to remove, but don't fail if it doesn't exist (e.g. tool failed to create it)
		_ = os.Remove(tmpFile)
	}()

	args := append(preArgs, outputSwitch, tmpFile)
	cmd := exec.Command(c.toolPath, args...)
	
	// Capture stderr in case of tool error
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// If command fails, still try to read file? No, command failure usually means no output.
		// ControlMyMonitor might return exit code 0 even on failure?
		return nil, fmt.Errorf("command execution failed: %w; stderr: %s", err, stderr.String())
	}

	// Read the file
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		// If file doesn't exist, it means the tool didn't output anything
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("tool did not create output file: %s", tmpFile)
		}
		return nil, fmt.Errorf("failed to read output file: %w", err)
	}
	
	return data, nil
}

// ListMonitors returns all connected monitors
func (c *windowsController) ListMonitors() ([]Monitor, error) {
	// Use /smonitors with a temp file
	outputBytes, err := c.runWithTempFile("/smonitors")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}

	// Decode potential UTF-16LE output
	decodedOutput := decodeUTF16(outputBytes)

	monitors, err := c.parseMonitorList(decodedOutput)
	if err != nil {
		return monitors, err
	}

	// Optimize: Fetch details for all monitors in parallel
	var wg sync.WaitGroup
	monitorsMutex := &sync.Mutex{} // Protects concurrent writes to monitors slice if needed? 
	// Actually writing to distinct indices monitors[i] is safe in Go, 
	// but let's be safe against race detector if any slices inside struct are modified.
	// However, we are modifying fields of struct, which is safe.
	
	for i := range monitors {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Check heuristic for missing metadata (Monitor 3 case)
			if monitors[idx].Name == "" && monitors[idx].Serial == "" {
				// We still try to fetch details, but we keep this flag in mind
			}

			supported, input, err := c.fetchMonitorDetails(monitors[idx].ID)
			
			monitorsMutex.Lock()
			monitors[idx].DDCSupported = supported
			if err == nil {
				monitors[idx].InputSource = input
				
				// Heuristic logic for the DP monitor showing as HDMI1
				if monitors[idx].Name == "" && monitors[idx].Serial == "" && monitors[idx].InputSource == InputSourceHDMI1 {
					log.Printf("DDC: Monitor %s has missing metadata and reports HDMI1. Applying heuristic -> DP1", monitors[idx].ID)
					monitors[idx].InputSource = InputSourceDP1
				}
				
				if !monitors[idx].DDCSupported {
					monitors[idx].DDCSupported = true
				}
			}
			monitorsMutex.Unlock()
		}(i)
	}
	wg.Wait()

	return monitors, nil
}

// getInputSourceFast tries to get input source using /GetValue which is faster than full dump.
// Returns (value, success).
func (c *windowsController) getInputSourceFast(id string) (int, bool) {
	// /GetValue returns the value in the exit code.
	// 0 usually means error or failure for input select (which is normally 15, 17, 27 etc).
	cmd := exec.Command(c.toolPath, "/GetValue", id, "60")
	err := cmd.Run()

	var exitCode int
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			// Execution failed (not found, permission denied, etc)
			return 0, false
		}
	} else {
		// Exit code 0
		exitCode = 0
	}

	// Valid input sources are usually non-zero (e.g. 15=DP, 17=HDMI)
	if exitCode > 0 {
		return exitCode, true
	}
	return 0, false
}

// fetchMonitorDetails gets DDC support status and current input in a single call
func (c *windowsController) fetchMonitorDetails(id string) (bool, InputSource, error) {
	// Optimization: Try /GetValue first (fast path)
	// This avoids the overhead of reading all VCP codes (~2-3s per monitor)
	if val, ok := c.getInputSourceFast(id); ok {
		log.Printf("DDC: Fast fetch successful for %s: %d", id, val)
		return true, InputSource(val), nil
	}

	// Fallback to full dump (slow path)
	// Run /scomma to get all VCP codes
	outputBytes, err := c.runWithTempFile("/scomma", "/Monitor", id)
	if err != nil {
		return false, 0, err
	}

	s := decodeUTF16(outputBytes)
	if len(s) < 10 {
		return false, 0, fmt.Errorf("empty or invalid output")
	}

	reader := csv.NewReader(strings.NewReader(s))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return false, 0, err
	}

	var supported bool
	var input InputSource
	foundInput := false

	for _, record := range records {
		if len(record) < 1 {
			continue
		}
		// Check for VCP 60 (Input) or 10 (Brightness) to confirm DDC support
		if record[0] == "60" || record[0] == "10" {
			supported = true
		}
		
		if record[0] == "60" && len(record) >= 4 {
			currentValStr := strings.TrimSpace(record[3])
			val, err := strconv.ParseInt(currentValStr, 10, 32)
			if err == nil {
				input = InputSource(val)
				foundInput = true
			}
		}
	}

	if !foundInput {
		return supported, 0, fmt.Errorf("input source (VCP 60) not found")
	}

	return supported, input, nil
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
		// 1. Monitor ID (MONITOR\...) - The most specific hardware identifier.
		//    We MUST use this for monitors that share the same model/name or have driver issues (like "ghost" monitors)
		//    because ControlMyMonitor aliases them if we use generic names or OS paths.
		// 2. Device Name (\\.\DISPLAY1\Monitor0) - Easier to read but can alias if driver didn't map correctly.
		// 3. Monitor Name (Model name) - Least reliable.

		id := currentProps["Monitor ID"]
		if id == "" {
			id = currentProps["Device Name"]
		}
		if id == "" {
			id = currentProps["Monitor Name"]
		}

		if id != "" {
			// Keep Monitor Name and Device Name as separate fields.
			// Use Monitor ID as the primary ID to ensure correct addressing of specific hardware instances.
			monitors = append(monitors, Monitor{
				ID:         id,
				Name:       currentProps["Monitor Name"],
				DeviceName: currentProps["Device Name"],
				Serial:     currentProps["Serial Number"],
			})
			dispName := currentProps["Monitor Name"]
			if dispName == "" {
				dispName = currentProps["Device Name"]
			}
			if dispName == "" {
				dispName = "Unknown Monitor"
			}
			fmt.Printf("DEBUG: Detected monitor: %s (ID: %s)\n", dispName, id)
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
			if r < 32 || r > 126 { // Filter non-printable
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
		// Match more-specific keys first to avoid accidental overwrites
		if strings.Contains(key, "Short Monitor ID") {
			currentProps["Short Monitor ID"] = val
		} else if strings.Contains(key, "Device Name") {
			currentProps["Device Name"] = val
		} else if strings.Contains(key, "Monitor ID") {
			currentProps["Monitor ID"] = val
		} else if strings.Contains(key, "Monitor Name") {
			currentProps["Monitor Name"] = val
		} else if strings.Contains(key, "Serial Number") {
			currentProps["Serial Number"] = val
		}
	}
	commit() // Don't forget the last one

	fmt.Printf("DEBUG: Found %d monitors total\n", len(monitors))
	return monitors, scanner.Err()
}

// GetCurrentInput gets the current input source for a monitor
func (c *windowsController) GetCurrentInput(monitorID string) (InputSource, error) {
	// Use /Monitor <ID> /scomma <file> to get settings
	outputBytes, err := c.runWithTempFile("/scomma", "/Monitor", monitorID)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}

	s := decodeUTF16(outputBytes)
	
	// Parse CSV
	reader := csv.NewReader(strings.NewReader(s))
	// Allow for variable number of fields if the tool behavior changes
	reader.FieldsPerRecord = -1 
	
	records, err := reader.ReadAll()
	if err != nil {
		return 0, fmt.Errorf("failed to parse CSV output: %w", err)
	}

	// Look for VCP code 60 (Input Select)
	// Default CSV format: VCP Code, VCP Code Name, Read-Write, Current Value, ...
	// records[0] is header usually
	
	for _, record := range records {
		if len(record) < 4 {
			continue
		}
		// Check VCP code (first column)
		if strings.TrimSpace(record[0]) == "60" {
			currentValStr := strings.TrimSpace(record[3])
			val, err := strconv.ParseInt(currentValStr, 10, 32)
			if err == nil {
				return InputSource(val), nil
			}
		}
	}

	return 0, fmt.Errorf("input source (VCP 60) not found")
}

// SetInputSource switches a monitor to the specified input
func (c *windowsController) SetInputSource(monitorID string, source InputSource) error {
	// VCP code 0x60 is the standard input select code
	args := []string{"/SetValue", monitorID, "60", fmt.Sprintf("%d", source)}

	// Use %q to see exactly what characters are in the ID string (escapes shown)
	log.Printf("DDC: Executing %s with ID %q and input %d", c.toolPath, monitorID, source)

	cmd := exec.Command(c.toolPath, args...)
	output, err := cmd.CombinedOutput()
	decoded := decodeUTF16(output)
	if err != nil {
		log.Printf("DDC: ControlMyMonitor failed for ID %q. Output: %s", monitorID, decoded)
		return fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}
	if decoded != "" {
		log.Printf("DDC: ControlMyMonitor output for ID %q: %s", monitorID, decoded)
	}
	return nil
}

// SetPower sets the monitor power state
func (c *windowsController) SetPower(monitorID string, on bool) error {
	// VCP code 0xD6 is Power Mode. 1 = On, 4 = Off/Standby
	val := "4"
	if on {
		val = "1"
	}
	args := []string{"/SetValue", monitorID, "D6", val}

	log.Printf("DDC: Setting power for ID %q to %s (D6)", monitorID, val)

	cmd := exec.Command(c.toolPath, args...)
	output, err := cmd.CombinedOutput()
	decoded := decodeUTF16(output)
	if err != nil {
		log.Printf("DDC: ControlMyMonitor failed for ID %q. Output: %s", monitorID, decoded)
		return fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}
	if decoded != "" {
		log.Printf("DDC: ControlMyMonitor output for ID %q: %s", monitorID, decoded)
	}
	return nil
}

// TestDDCSupport tests if a monitor supports DDC/CI by trying multiple VCP codes
func (c *windowsController) TestDDCSupport(monitorID string) bool {
	// Use /scomma to dump values. If we get a valid dump for 60 or 10, it's supported.
	outputBytes, err := c.runWithTempFile("/scomma", "/Monitor", monitorID)
	if err != nil {
		log.Printf("DDC: TestDDCSupport failed to run tool: %v", err)
		return false
	}
	
	s := decodeUTF16(outputBytes)
	if len(s) < 10 { // Too short to be valid
		return false
	}
	
	// Check if we have VCP 60 or 10 in the CSV
	reader := csv.NewReader(strings.NewReader(s))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return false
	}
	
	for _, record := range records {
		if len(record) > 0 {
			if record[0] == "60" || record[0] == "10" {
				return true
			}
		}
	}
	
	return false
}

