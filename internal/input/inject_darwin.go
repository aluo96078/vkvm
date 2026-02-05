//go:build darwin

package input

import (
	"fmt"
	"log"
)

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework ApplicationServices

#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <ApplicationServices/ApplicationServices.h>
#include <math.h>

// Check if we have accessibility permissions
bool hasAccessibilityPermissions() {
    return AXIsProcessTrusted();
}

// Get current mouse position
CGPoint getCurrentMousePosition() {
    CGEventRef event = CGEventCreate(NULL);
    CGPoint cursor = CGEventGetLocation(event);
    CFRelease(event);
    return cursor;
}

// Track button state for drag operations
static int g_leftButtonDown = 0;
static int g_rightButtonDown = 0;
static int g_middleButtonDown = 0;

// Track click state for double-click detection
static CFAbsoluteTime g_lastClickTime = 0;
static CGPoint g_lastClickPos = {0, 0};
static int g_clickCount = 0;

// Helper functions - inject mouse move with relative delta
void injectMouseMoveRelative(CGFloat dx, CGFloat dy) {
    // Get current mouse position
    CGPoint currentPos = getCurrentMousePosition();

    // Calculate new position
    CGPoint newPos = CGPointMake(currentPos.x + dx, currentPos.y + dy);

    // Determine event type based on button state (drag vs move)
    CGEventType eventType;
    CGMouseButton button = kCGMouseButtonLeft;

    if (g_leftButtonDown) {
        eventType = kCGEventLeftMouseDragged;
        button = kCGMouseButtonLeft;
    } else if (g_rightButtonDown) {
        eventType = kCGEventRightMouseDragged;
        button = kCGMouseButtonRight;
    } else if (g_middleButtonDown) {
        eventType = kCGEventOtherMouseDragged;
        button = kCGMouseButtonCenter;
    } else {
        eventType = kCGEventMouseMoved;
        button = kCGMouseButtonLeft;
    }

    // Create mouse event
    CGEventRef event = CGEventCreateMouseEvent(NULL, eventType, newPos, button);
    if (event) {
        // Set relative delta values for proper mouse acceleration handling
        CGEventSetIntegerValueField(event, kCGMouseEventDeltaX, (int64_t)dx);
        CGEventSetIntegerValueField(event, kCGMouseEventDeltaY, (int64_t)dy);
        CGEventPost(kCGSessionEventTap, event);
        CFRelease(event);
    }
}

void injectMouseButton(int button, bool pressed) {
    CGMouseButton cgButton;
    CGEventType eventType;

    switch (button) {
        case 1: cgButton = kCGMouseButtonLeft; break;
        case 2: cgButton = kCGMouseButtonRight; break;
        case 3: cgButton = kCGMouseButtonCenter; break;
        case 4: cgButton = 3; break;  // XButton1 -> Button 3 (extra button)
        case 5: cgButton = 4; break;  // XButton2 -> Button 4 (extra button)
        default: return;
    }

    if (pressed) {
        switch (button) {
            case 1: eventType = kCGEventLeftMouseDown; break;
            case 2: eventType = kCGEventRightMouseDown; break;
            case 3:
            case 4:
            case 5: eventType = kCGEventOtherMouseDown; break;
            default: return;
        }
    } else {
        switch (button) {
            case 1: eventType = kCGEventLeftMouseUp; break;
            case 2: eventType = kCGEventRightMouseUp; break;
            case 3:
            case 4:
            case 5: eventType = kCGEventOtherMouseUp; break;
            default: return;
        }
    }

    // Update button state for drag detection
    if (button == 1) g_leftButtonDown = pressed ? 1 : 0;
    else if (button == 2) g_rightButtonDown = pressed ? 1 : 0;
    else if (button == 3) g_middleButtonDown = pressed ? 1 : 0;

    // Get current mouse position for button events
    CGPoint currentPos = getCurrentMousePosition();

    // Handle click count for double-click detection
    int clickCount = 1;
    if (pressed && button == 1) {
        CFAbsoluteTime now = CFAbsoluteTimeGetCurrent();
        CGFloat distance = sqrt(pow(currentPos.x - g_lastClickPos.x, 2) + pow(currentPos.y - g_lastClickPos.y, 2));

        // Double-click threshold: 300ms and within 5 pixels
        if ((now - g_lastClickTime) < 0.3 && distance < 5.0) {
            g_clickCount++;
        } else {
            g_clickCount = 1;
        }

        clickCount = g_clickCount;
        g_lastClickTime = now;
        g_lastClickPos = currentPos;
    }

    CGEventRef event = CGEventCreateMouseEvent(NULL, eventType, currentPos, cgButton);
    if (event) {
        // Set click count for proper double-click/triple-click recognition
        CGEventSetIntegerValueField(event, kCGMouseEventClickState, clickCount);
        // Set button number for XButton events
        if (button >= 3) {
            CGEventSetIntegerValueField(event, kCGMouseEventButtonNumber, cgButton);
        }
        CGEventPost(kCGSessionEventTap, event);
        CFRelease(event);
    }
}

// Scroll wheel injection: vertical and horizontal
void injectMouseWheel(int deltaY, int deltaX) {
    // CGEventCreateScrollWheelEvent uses scroll units (typically lines)
    // Windows WHEEL_DELTA is 120 for one notch, so we normalize
    int32_t scrollDeltaY = deltaY / 120;  // Convert to scroll lines
    int32_t scrollDeltaX = deltaX / 120;

    // If delta is less than one unit but non-zero, still scroll at least one unit
    if (deltaY != 0 && scrollDeltaY == 0) {
        scrollDeltaY = deltaY > 0 ? 1 : -1;
    }
    if (deltaX != 0 && scrollDeltaX == 0) {
        scrollDeltaX = deltaX > 0 ? 1 : -1;
    }

    CGEventRef event = CGEventCreateScrollWheelEvent(
        NULL,
        kCGScrollEventUnitLine,
        2,  // wheel count: 2 for both vertical and horizontal
        scrollDeltaY,
        scrollDeltaX
    );
    if (event) {
        CGEventPost(kCGSessionEventTap, event);
        CFRelease(event);
    }
}

void injectKey(CGKeyCode keyCode, bool pressed, uint16 modifiers) {
    // Check if this is a modifier key
    bool isModifierKey = false;
    CGEventFlags modifierFlag = 0;

    switch (keyCode) {
        case 0x38: // Left Shift
        case 0x3C: // Right Shift
            isModifierKey = true;
            modifierFlag = kCGEventFlagMaskShift;
            break;
        case 0x3B: // Left Control
        case 0x3E: // Right Control
            isModifierKey = true;
            modifierFlag = kCGEventFlagMaskControl;
            break;
        case 0x3A: // Left Option
        case 0x3D: // Right Option
            isModifierKey = true;
            modifierFlag = kCGEventFlagMaskAlternate;
            break;
        case 0x37: // Left Command
        case 0x36: // Right Command
            isModifierKey = true;
            modifierFlag = kCGEventFlagMaskCommand;
            break;
    }

    if (isModifierKey) {
        // For modifier keys, use kCGEventFlagsChanged
        CGEventRef event = CGEventCreate(NULL);
        if (event) {
            CGEventFlags currentFlags = CGEventGetFlags(event);
            CFRelease(event);

            CGEventFlags newFlags;
            if (pressed) {
                newFlags = currentFlags | modifierFlag;
            } else {
                newFlags = currentFlags & ~modifierFlag;
            }

            // Create a flags changed event
            CGEventRef flagsEvent = CGEventCreateKeyboardEvent(NULL, keyCode, pressed);
            if (flagsEvent) {
                CGEventSetType(flagsEvent, kCGEventFlagsChanged);
                CGEventSetFlags(flagsEvent, newFlags);
                CGEventPost(kCGSessionEventTap, flagsEvent);
                CFRelease(flagsEvent);
            }
        }
    } else {
        // For regular keys, use normal key events
        CGEventType eventType = pressed ? kCGEventKeyDown : kCGEventKeyUp;
        CGEventRef event = CGEventCreateKeyboardEvent(NULL, keyCode, pressed);
        if (event) {
            // Set modifier flags
            CGEventFlags flags = 0;
            if (modifiers & 0x0001) flags |= kCGEventFlagMaskShift;     // Shift
            if (modifiers & 0x0002) flags |= kCGEventFlagMaskControl;   // Ctrl
            if (modifiers & 0x0004) flags |= kCGEventFlagMaskAlternate; // Alt
            if (modifiers & 0x0008) flags |= kCGEventFlagMaskCommand;   // Cmd

            CGEventSetFlags(event, flags);
            CGEventPost(kCGSessionEventTap, event);
            CFRelease(event);
        }
    }
}
*/
import "C"

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
	0x7C: 0x69, // F13
	0x7D: 0x6B, // F14
	0x7E: 0x71, // F15
	0x7F: 0x6A, // F16
	0x80: 0x40, // F17
	0x81: 0x4F, // F18
	0x82: 0x50, // F19
	0x83: 0x5A, // F20

	// Special keys
	0x08: 0x33, // Backspace -> Delete
	0x09: 0x30, // Tab
	0x0D: 0x24, // Enter/Return
	0x10: 0x38, // Shift (left)
	0x11: 0x3B, // Control (left)
	0x12: 0x3A, // Alt -> Option
	0x13: 0x48, // Pause -> Pause
	0x14: 0x39, // Caps Lock
	0x1B: 0x35, // Escape
	0x20: 0x31, // Space
	0x2C: 0x5D, // Print Screen -> Print Screen
	0x2D: 0x72, // Insert -> Insert (Help key on some keyboards)
	0x2E: 0x75, // Delete -> Forward Delete
	0x5D: 0x2F, // Apps -> Context Menu (right click menu)
	0x90: 0x47, // Num Lock
	0x91: 0x57, // Scroll Lock

	// Media keys (extended VK codes)
	0xAD: 0x49, // Volume Mute
	0xAE: 0x4A, // Volume Down
	0xAF: 0x48, // Volume Up
	0xB0: 0x34, // Next Track
	0xB1: 0x31, // Previous Track
	0xB2: 0x42, // Stop
	0xB3: 0x43, // Play/Pause
	0xB4: 0x5E, // Start Mail
	0xB5: 0x5C, // Select Media

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
type Injector struct {
	modifierState uint16 // Track current modifier state
}

// NewInjector creates a new input injector for macOS
func NewInjector() *Injector {
	return &Injector{}
}

// InjectMouseMove injects a mouse movement event
func (i *Injector) InjectMouseMove(dx, dy int) error {
	// Skip zero movement
	if dx == 0 && dy == 0 {
		return nil
	}

	// Use C function for proper relative mouse movement
	C.injectMouseMoveRelative(C.CGFloat(dx), C.CGFloat(dy))
	return nil
}

// InjectMouseButton injects a mouse button event
func (i *Injector) InjectMouseButton(button int, pressed bool) error {
	if button < 1 || button > 5 {
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

// InjectMouseWheel injects a mouse scroll wheel event
// deltaY: positive=up, negative=down (vertical scroll)
// deltaX: positive=right, negative=left (horizontal scroll)
func (i *Injector) InjectMouseWheel(deltaY, deltaX int) error {
	C.injectMouseWheel(C.int(deltaY), C.int(deltaX))
	return nil
}

// InjectKey injects a keyboard event
func (i *Injector) InjectKey(keyCode uint16, pressed bool, modifiers uint16) error {
	macKeyCode, ok := windowsToMacKeyMap[keyCode]
	if !ok {
		log.Printf("Warning: unmapped key code 0x%X", keyCode)
		return fmt.Errorf("unmapped key code: 0x%X", keyCode)
	}

	// Update local modifier state
	switch keyCode {
	case 0x10, 0xA0, 0xA1: // Shift keys
		if pressed {
			i.modifierState |= 0x01
		} else {
			i.modifierState &^= 0x01
		}
	case 0x11, 0xA2, 0xA3: // Control keys
		if pressed {
			i.modifierState |= 0x02
		} else {
			i.modifierState &^= 0x02
		}
	case 0x12, 0xA4, 0xA5: // Alt keys
		if pressed {
			i.modifierState |= 0x04
		} else {
			i.modifierState &^= 0x04
		}
	case 0x5B, 0x5C: // Windows/Command keys
		if pressed {
			i.modifierState |= 0x08
		} else {
			i.modifierState &^= 0x08
		}
	}

	// Use the C function for injection with local modifier state
	C.injectKey(C.CGKeyCode(macKeyCode), C.bool(pressed), C.uint16(i.modifierState))
	return nil
}

// TestKeyMapping tests if a Windows key code can be mapped to macOS
func TestKeyMapping(windowsKeyCode uint16) (uint16, bool) {
	macKeyCode, ok := windowsToMacKeyMap[windowsKeyCode]
	return macKeyCode, ok
}

// GetMappedKeys returns a list of all mapped Windows key codes for testing
func GetMappedKeys() []uint16 {
	keys := make([]uint16, 0, len(windowsToMacKeyMap))
	for k := range windowsToMacKeyMap {
		keys = append(keys, k)
	}
	return keys
}
