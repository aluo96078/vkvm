//go:build darwin

package input

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework ApplicationServices

#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <ApplicationServices/ApplicationServices.h>

// Check if we have accessibility permissions
bool hasAccessibilityPermissions() {
    return AXIsProcessTrusted();
}

// Get current mouse position
CGPoint getCurrentMousePosition() {
    CGEventRef event = CGEventCreate(NULL);
    CGPoint cursor = CGEventGetLocation(event);
    CFRelease(event);
    return cursor;vu04y94
    vmpcl5jbj
    5/t;xk
    <u6tj6
    vu;ej0
    logu3wu6elvu/s/
}

// Helper functions - inject mouse move with relative delta
void injectMouseMove(CGFloat dx, CGFloat dy) {
    // Get current mouse position
    CGPoint currentPos = getCurrentMousePosition();
    
    // Calculate new position
    CGPoint newPo
    
    
    s = CGPointMake(currentPos.x + dx, currentPos.y + dy);
    
    // Create mouse moved event at the new position
    CGEventRef event = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, newPos, kCGMouseButtonLeft);
    CGEventPost(kCGSessionEventTap, event);
    CFRelease(event);
}

void injectMouseButton(int button, bool pressed) {
    CGMouseButton cgButton;
    CGEventType eventType;

    switch (button) {
        case 1: cgButton = kCGMouseButtonLeft; break;
        case 2: cgButton = kCGMouseButtonRight; break;
        case 3: cgButton = kCGMouseButtonCenter; break;
        default: return;
    }

    if (pressed) {
        switch (button) {
            case 1: eventType = kCGEventLeftMouseDown; break;
            case 2: eventType = kCGEventRightMouseDown; break;
            case 3: eventType = kCGEventOtherMouseDown; break;
            default: return;
        }
    } else {
        switch (button) {
            case 1: eventType = kCGEventLeftMouseUp; break;
            case 2: eventType = kCGEventRightMouseUp; break;
            case 3: eventType = kCGEventOtherMouseUp; break;
            default: return;
        }
    }

    // Get current mouse position for button events
    CGPoint currentPos = getCurrentMousePosition();
    CGEventRef event = CGEventCreateMouseEvent(NULL, eventType, currentPos, cgButton);
    CGEventPost(kCGSessionEventTap, event);
    CFRelease(event);
}

void injectKey(CGKeyCode keyCode, bool pressed) {
    CGEventType eventType = pressed ? kCGEventKeyDown : kCGEventKeyUp;
    CGEventRef event = CGEventCreateKeyboardEvent(NULL, keyCode, pressed);
    CGEventPost(kCGSessionEventTap, event);
    CFRelease(event);
}
*/
import "C"
import (
	"fmt"
)

// macOS implementation of input injection using CoreGraphics

// Windows VK code to macOS CGKeyCode mapping
// Reference: https://docs.microsoft.com/en-us/windows/win32/inputdev/virtual-key-codes
// Reference: https://developer.apple.com/documentation/coregraphics/cgkeycode
var windowsToMacKeyMap = map[uint16]uint16{
	// Letters A-Z (Windows VK_A = 0x41, macOS kVK_ANSI_A = 0x00)
	0x41: 0x00, // A
	0x42: 0x0B, // B
	0x43: 0x08, // C
	0x44: 0x02, // D
	0x45: 0x0E, // E
	0x46: 0x03, // F
	0x47: 0x05, // G
	0x48: 0x04, // H
	0x49: 0x22, // I
	0x4A: 0x26, // J
	0x4B: 0x28, // K
	0x4C: 0x25, // L
	0x4D: 0x2E, // M
	0x4E: 0x2D, // N
	0x4F: 0x1F, // O
	0x50: 0x23, // P
	0x51: 0x0C, // Q
	0x52: 0x0F, // R
	0x53: 0x01, // S
	0x54: 0x11, // T
	0x55: 0x20, // U
	0x56: 0x09, // V
	0x57: 0x0D, // W
	0x58: 0x07, // X
	0x59: 0x10, // Y
	0x5A: 0x06, // Z

	// Numbers 0-9 (Windows VK_0 = 0x30, macOS kVK_ANSI_0 = 0x1D)
	0x30: 0x1D, // 0
	0x31: 0x12, // 1
	0x32: 0x13, // 2
	0x33: 0x14, // 3
	0x34: 0x15, // 4
	0x35: 0x17, // 5
	0x36: 0x16, // 6
	0x37: 0x1A, // 7
	0x38: 0x1C, // 8
	0x39: 0x19, // 9

	// Function keys (Windows VK_F1 = 0x70, macOS kVK_F1 = 0x7A)
	0x70: 0x7A, // F1
	0x71: 0x78, // F2
	0x72: 0x63, // F3
	0x73: 0x76, // F4
	0x74: 0x60, // F5
	0x75: 0x61, // F6
	0x76: 0x62, // F7
	0x77: 0x64, // F8
	0x78: 0x65, // F9
	0x79: 0x6D, // F10
	0x7A: 0x67, // F11
	0x7B: 0x6F, // F12

	// Special keys
	0x08: 0x33, // Backspace -> Delete
	0x09: 0x30, // Tab
	0x0D: 0x24, // Enter/Return
	0x10: 0x38, // Shift (left)
	0x11: 0x3B, // Control (left)
	0x12: 0x3A, // Alt -> Option
	0x14: 0x39, // Caps Lock
	0x1B: 0x35, // Escape
	0x20: 0x31, // Space

	// Arrow keys
	0x25: 0x7B, // Left Arrow
	0x26: 0x7E, // Up Arrow
	0x27: 0x7C, // Right Arrow
	0x28: 0x7D, // Down Arrow

	// Navigation keys
	0x21: 0x74, // Page Up
	0x22: 0x79, // Page Down
	0x23: 0x77, // End
	0x24: 0x73, // Home
	0x2D: 0x72, // Insert -> Help
	0x2E: 0x75, // Delete -> Forward Delete

	// Modifier keys
	0x5B: 0x37, // Left Windows -> Left Command
	0x5C: 0x36, // Right Windows -> Right Command
	0xA0: 0x38, // Left Shift
	0xA1: 0x3C, // Right Shift
	0xA2: 0x3B, // Left Control
	0xA3: 0x3E, // Right Control
	0xA4: 0x3A, // Left Alt -> Left Option
	0xA5: 0x3D, // Right Alt -> Right Option

	// Punctuation and symbols
	0xBA: 0x29, // ; -> ;
	0xBB: 0x18, // = -> =
	0xBC: 0x2B, // , -> ,
	0xBD: 0x1B, // - -> -
	0xBE: 0x2F, // . -> .
	0xBF: 0x2C, // / -> /
	0xC0: 0x32, // ` -> `
	0xDB: 0x21, // [ -> [
	0xDC: 0x2A, // \\ -> \\
	0xDD: 0x1E, // ] -> ]
	0xDE: 0x27, // ' -> '

	// Numpad
	0x60: 0x52, // Numpad 0
	0x61: 0x53, // Numpad 1
	0x62: 0x54, // Numpad 2
	0x63: 0x55, // Numpad 3
	0x64: 0x56, // Numpad 4
	0x65: 0x57, // Numpad 5
	0x66: 0x58, // Numpad 6
	0x67: 0x59, // Numpad 7
	0x68: 0x5B, // Numpad 8
	0x69: 0x5C, // Numpad 9
	0x6A: 0x43, // Numpad *
	0x6B: 0x45, // Numpad +
	0x6D: 0x4E, // Numpad -
	0x6E: 0x41, // Numpad .
	0x6F: 0x4B, // Numpad /
}

// Injector represents a macOS input injector
type Injector struct{}

// NewInjector creates a new input injector for macOS
func NewInjector() *Injector {
	return &Injector{}
}

// InjectMouseMove injects a mouse movement event
func (i *Injector) InjectMouseMove(dx, dy int) error {
	C.injectMouseMove(C.CGFloat(dx), C.CGFloat(dy))
	return nil
}

// InjectMouseButton injects a mouse button event
func (i *Injector) InjectMouseButton(button int, pressed bool) error {
	if button < 1 || button > 3 {
		return fmt.Errorf("invalid button number: %d", button)
	}

	var cPressed C.bool
	if pressed {
		cPressed = C.bool(true)
	} else {
		cPressed = C.bool(false)
	}

	C.injectMouseButton(C.int(button), cPressed)
	return nil
}

// InjectKey injects a keyboard event with Windows VK code to macOS key code conversion
func (i *Injector) InjectKey(keyCode uint16, pressed bool, modifiers uint16) error {
	// Convert Windows VK code to macOS CGKeyCode
	macKeyCode, ok := windowsToMacKeyMap[keyCode]
	if !ok {
		macKeyCode = keyCode // Fallback: pass through (likely won't work correctly)
	}

	var cPressed C.bool
	if pressed {
		cPressed = C.bool(true)
	} else {
		cPressed = C.bool(false)
	}

	C.injectKey(C.CGKeyCode(macKeyCode), cPressed)
	return nil
}
