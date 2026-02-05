//go:build darwin

package hotkey

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework ApplicationServices
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdint.h>

// Forward declaration of the callback
CGEventRef eventCallback(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *refcon);

// Helper to start the loop - takes uintptr_t to avoid Go unsafe.Pointer conversion
static inline void startEventTap(uintptr_t refcon) {
    CGEventMask mask = kCGEventMaskForAllEvents;
    CFMachPortRef tap = CGEventTapCreate(
        kCGSessionEventTap,
        kCGHeadInsertEventTap,
        kCGEventTapOptionListenOnly,
        mask,
        eventCallback,
        (void*)refcon
    );

    if (!tap) {
        printf("Hotkey Engine: ERROR! Failed to create CGEventTap. Accessibility permissions missing?\n");
        return;
    }

    CFRunLoopSourceRef source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0);
    CFRunLoopAddSource(CFRunLoopGetCurrent(), source, kCFRunLoopCommonModes);
    CGEventTapEnable(tap, true);
    printf("Hotkey Engine: CGEventTap successfully initialized.\n");
    CFRunLoopRun();
}
*/
import "C"
import (
	"log"
	"runtime/cgo"
	"strconv"
	"strings"
	"unsafe"
)

//export eventCallback
func eventCallback(proxy C.CGEventTapProxy, eventType C.CGEventType, event C.CGEventRef, refcon unsafe.Pointer) C.CGEventRef {
	h := cgo.Handle(uintptr(refcon))
	m := h.Value().(*Manager)

	switch eventType {
	case C.kCGEventKeyDown, C.kCGEventKeyUp:
		isDown := eventType == C.kCGEventKeyDown
		keyCode := uint16(C.CGEventGetIntegerValueField(event, C.kCGKeyboardEventKeycode))
		keyName := macKeyCodeToName(keyCode)
		if keyName != "" {
			m.UpdateState(keyName, isDown)
		}

	case C.kCGEventFlagsChanged:
		// Handle modifier key state changes
		flags := C.CGEventGetFlags(event)
		keyCode := uint16(C.CGEventGetIntegerValueField(event, C.kCGKeyboardEventKeycode))

		// Determine which modifier and its state based on keycode and flags
		switch keyCode {
		case 55, 54: // Command keys
			m.UpdateState("CMD", (flags&C.kCGEventFlagMaskCommand) != 0)
		case 56, 60: // Shift keys
			m.UpdateState("SHIFT", (flags&C.kCGEventFlagMaskShift) != 0)
		case 58, 61: // Alt/Option keys
			m.UpdateState("ALT", (flags&C.kCGEventFlagMaskAlternate) != 0)
		case 59, 62: // Control keys
			m.UpdateState("CTRL", (flags&C.kCGEventFlagMaskControl) != 0)
		}

	case C.kCGEventLeftMouseDown, C.kCGEventLeftMouseUp,
		C.kCGEventRightMouseDown, C.kCGEventRightMouseUp,
		C.kCGEventOtherMouseDown, C.kCGEventOtherMouseUp:

		isDown := (eventType == C.kCGEventLeftMouseDown ||
			eventType == C.kCGEventRightMouseDown ||
			eventType == C.kCGEventOtherMouseDown)

		btnNumber := int64(C.CGEventGetIntegerValueField(event, C.kCGMouseEventButtonNumber))
		btnName := ""
		switch btnNumber {
		case 0:
			btnName = "MOUSE1"
		case 1:
			btnName = "MOUSE3" // Right
		case 2:
			btnName = "MOUSE2" // Middle
		case 3:
			btnName = "MOUSE4"
		case 4:
			btnName = "MOUSE5"
		default:
			btnName = "MOUSE" + strings.TrimSpace(strconv.FormatInt(btnNumber+1, 10))
		}

		if btnName != "" {
			m.UpdateState(btnName, isDown)
		}
	}

	return event
}

func (m *Manager) startPlatform() error {
	handle := cgo.NewHandle(m)
	go func() {
		log.Println("Hotkey Engine: macOS CGEventTap started.")
		C.startEventTap(C.uintptr_t(handle))
	}()
	return nil
}

func macKeyCodeToName(code uint16) string {
	switch code {
	case 55, 54:
		return "CMD"
	case 56, 60:
		return "SHIFT"
	case 58, 61:
		return "ALT"
	case 59, 62:
		return "CTRL"
	case 49:
		return "SPACE"
	case 36:
		return "ENTER"
	case 53:
		return "ESC"

	case 0:
		return "A"
	case 11:
		return "B"
	case 8:
		return "C"
	case 2:
		return "D"
	case 14:
		return "E"
	case 3:
		return "F"
	case 5:
		return "G"
	case 4:
		return "H"
	case 34:
		return "I"
	case 38:
		return "J"
	case 40:
		return "K"
	case 37:
		return "L"
	case 46:
		return "M"
	case 45:
		return "N"
	case 31:
		return "O"
	case 35:
		return "P"
	case 12:
		return "Q"
	case 15:
		return "R"
	case 1:
		return "S"
	case 17:
		return "T"
	case 32:
		return "U"
	case 9:
		return "V"
	case 13:
		return "W"
	case 7:
		return "X"
	case 16:
		return "Y"
	case 6:
		return "Z"

	case 29:
		return "0"
	case 18:
		return "1"
	case 19:
		return "2"
	case 20:
		return "3"
	case 21:
		return "4"
	case 23:
		return "5"
	case 22:
		return "6"
	case 26:
		return "7"
	case 28:
		return "8"
	case 25:
		return "9"

	case 122:
		return "F1"
	case 120:
		return "F2"
	case 99:
		return "F3"
	case 118:
		return "F4"
	case 96:
		return "F5"
	case 97:
		return "F6"
	case 98:
		return "F7"
	case 100:
		return "F8"
	case 101:
		return "F9"
	case 109:
		return "F10"
	case 103:
		return "F11"
	case 111:
		return "F12"
	}
	return ""
}
