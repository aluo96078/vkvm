// Package hid contains host-side HID report framing shared by the optional
// ESP32-S3 USB/BLE backend. It does not claim hardware support; reports are
// deterministic and can be tested without a board or a macOS driver.
package hid

import (
	"errors"
	"math"
)

const (
	KeyboardReportSize = 8
	MouseReportSize    = 4
)

var ErrKeyboardRollover = errors.New("more than six simultaneous boot-keyboard keys")

type KeyboardReport [KeyboardReportSize]byte
type MouseReport [MouseReportSize]byte

func EncodeKeyboard(modifiers byte, keys []byte) (KeyboardReport, error) {
	var report KeyboardReport
	report[0] = modifiers
	seen := make(map[byte]struct{}, len(keys))
	pos := 2
	for _, key := range keys {
		if key == 0 {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if pos >= len(report) {
			return KeyboardReport{}, ErrKeyboardRollover
		}
		report[pos] = key
		pos++
	}
	return report, nil
}

func EncodeMouse(buttons byte, deltaX, deltaY, wheel int) MouseReport {
	return MouseReport{buttons, byte(clampInt8(deltaX)), byte(clampInt8(deltaY)), byte(clampInt8(wheel))}
}

// SplitRelativeDelta converts an arbitrary raw delta into boot-mouse reports
// without losing distance. This avoids int8 wraparound on high-polling-rate
// devices while retaining bounded report sizes.
func SplitRelativeDelta(deltaX, deltaY int) []MouseReport {
	steps := max(abs(deltaX), abs(deltaY))
	if steps == 0 {
		return []MouseReport{{}}
	}
	count := (steps + 127) / 127
	reports := make([]MouseReport, 0, count)
	remainingX, remainingY := deltaX, deltaY
	for remainingX != 0 || remainingY != 0 {
		x, y := clampInt8(remainingX), clampInt8(remainingY)
		reports = append(reports, EncodeMouse(0, int(x), int(y), 0))
		remainingX -= int(x)
		remainingY -= int(y)
	}
	return reports
}

func clampInt8(value int) int8 {
	if value > math.MaxInt8 {
		return math.MaxInt8
	}
	if value < math.MinInt8 {
		return math.MinInt8
	}
	return int8(value)
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
