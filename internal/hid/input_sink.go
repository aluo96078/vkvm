package hid

import "sync"

// BLEInputSink translates VKVM's Windows-style event vocabulary into boot HID
// reports while retaining physical key/button state for release-all recovery.
type BLEInputSink struct {
	session  *BLESession
	mu       sync.Mutex
	keys     map[byte]struct{}
	buttons  byte
	modifier byte
}

func NewBLEInputSink(session *BLESession) *BLEInputSink {
	return &BLEInputSink{session: session, keys: make(map[byte]struct{})}
}

func (s *BLEInputSink) Send(event InputEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return ErrBLEUnavailable
	}
	switch event.Type {
	case "key":
		usage, ok := VKToHIDUsageWithScan(event.KeyCode, event.ScanCode, event.Extended)
		if !ok {
			return nil
		}
		if modifier, isModifier := hidModifierForUsage(usage); isModifier {
			if event.Pressed {
				s.modifier |= modifier
			} else {
				s.modifier &^= modifier
			}
		} else {
			if event.Pressed {
				s.keys[usage] = struct{}{}
			} else {
				delete(s.keys, usage)
			}
		}
		keys := make([]byte, 0, len(s.keys))
		for key := range s.keys {
			keys = append(keys, key)
		}
		modifiers := s.modifier | convertModifierMask(event.Modifiers)
		report, err := EncodeKeyboard(modifiers, keys)
		if err != nil {
			return err
		}
		return s.session.SendKeyboard(report)
	case "mouse_move":
		for _, report := range SplitRelativeDelta(event.DeltaX, event.DeltaY) {
			report[0] = s.buttons
			if err := s.session.SendMouse(report); err != nil {
				return err
			}
		}
		return nil
	case "mouse_btn":
		mask := buttonMask(event.Button)
		if event.Pressed {
			s.buttons |= mask
		} else {
			s.buttons &^= mask
		}
		return s.session.SendMouse(EncodeMouse(s.buttons, 0, 0, 0))
	case "mouse_wheel":
		return s.session.SendMouse(EncodeMouse(s.buttons, 0, 0, event.Wheel))
	default:
		return nil
	}
}

func (s *BLEInputSink) ReleaseAll() error {
	s.mu.Lock()
	s.keys = make(map[byte]struct{})
	s.buttons = 0
	s.modifier = 0
	s.mu.Unlock()
	if s.session == nil {
		return ErrBLEUnavailable
	}
	return s.session.ReleaseAll()
}

func buttonMask(button int) byte {
	if button < 1 || button > 8 {
		return 0
	}
	return 1 << (button - 1)
}

// VKToHIDUsage maps the common Windows virtual-key range to USB HID usage
// IDs. Unknown keys are deliberately dropped rather than guessed, preventing
// media/Fn keys from producing an unrelated key on the target.
func VKToHIDUsage(vk uint16) (byte, bool) {
	return VKToHIDUsageWithScan(vk, 0, false)
}

// VKToHIDUsageWithScan uses the scan code to distinguish the generic Windows
// VK_SHIFT/VK_CONTROL/VK_MENU values. This preserves left/right modifiers
// when the host captured a Raw Input keyboard report.
func VKToHIDUsageWithScan(vk, scan uint16, extended bool) (byte, bool) {
	if vk == 0x10 { // VK_SHIFT
		if scan == 0x36 {
			return 0xE5, true
		}
		return 0xE1, true
	}
	if vk == 0x11 { // VK_CONTROL
		if extended {
			return 0xE4, true
		}
		return 0xE0, true
	}
	if vk == 0x12 { // VK_MENU/Alt
		if extended {
			return 0xE6, true
		}
		return 0xE2, true
	}
	switch {
	case vk >= 0x41 && vk <= 0x5A:
		return byte(0x04 + (vk - 0x41)), true
	case vk >= 0x31 && vk <= 0x39:
		return byte(0x1E + (vk - 0x31)), true
	case vk == 0x30:
		return 0x27, true
	case vk >= 0x70 && vk <= 0x7B: // F1-F12
		return byte(0x3A + (vk - 0x70)), true
	case vk >= 0x7C && vk <= 0x83: // F13-F20
		return byte(0x68 + (vk - 0x7C)), true
	case vk == 0x20:
		return 0x2C, true
	case vk == 0x0D:
		return 0x28, true
	case vk == 0x1B:
		return 0x29, true
	case vk == 0x08:
		return 0x2A, true
	case vk == 0x09:
		return 0x2B, true
	case vk == 0x14: // Caps Lock
		return 0x39, true
	case vk == 0xA0:
		return 0xE1, true
	case vk == 0xA1:
		return 0xE5, true
	case vk == 0xA2:
		return 0xE0, true
	case vk == 0xA3:
		return 0xE4, true
	case vk == 0xA4:
		return 0xE2, true
	case vk == 0xA5:
		return 0xE6, true
	case vk == 0x5B:
		return 0xE3, true
	case vk == 0x5C:
		return 0xE7, true
	case vk == 0x25:
		return 0x50, true
	case vk == 0x26:
		return 0x52, true
	case vk == 0x27:
		return 0x4F, true
	case vk == 0x28:
		return 0x51, true
	default:
		return 0, false
	}
}

func hidModifierForUsage(usage byte) (byte, bool) {
	switch usage {
	case 0xE0, 0xE4:
		return 0x01, true // Ctrl
	case 0xE1, 0xE5:
		return 0x02, true // Shift
	case 0xE2, 0xE6:
		return 0x04, true // Alt
	case 0xE3, 0xE7:
		return 0x08, true // GUI
	default:
		return 0, false
	}
}

// convertModifierMask converts VKVM's stable mask (Shift, Ctrl, Alt, GUI)
// into the HID boot-report ordering (Ctrl, Shift, Alt, GUI).
func convertModifierMask(mask byte) byte {
	var out byte
	if mask&0x01 != 0 {
		out |= 0x02
	}
	if mask&0x02 != 0 {
		out |= 0x01
	}
	if mask&0x04 != 0 {
		out |= 0x04
	}
	if mask&0x08 != 0 {
		out |= 0x08
	}
	return out
}
