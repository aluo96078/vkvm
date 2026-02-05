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

// Helper functions
void injectMouseMove(CGFloat dx, CGFloat dy) {
    CGEventRef event = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, CGPointZero, kCGMouseButtonLeft);
    CGEventSetIntegerValueField(event, kCGMouseEventDeltaX, (int64_t)dx);
    CGEventSetIntegerValueField(event, kCGMouseEventDeltaY, (int64_t)dy);
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

    CGEventRef event = CGEventCreateMouseEvent(NULL, eventType, CGPointZero, cgButton);
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

// Injector represents a macOS input injector
type Injector struct{}

// NewInjector creates a new input injector for macOS
func NewInjector() *Injector {
	// Check accessibility permissions
	if !C.hasAccessibilityPermissions() {
		fmt.Printf("[DARWIN-INJECT] WARNING: Accessibility permissions not granted!\n")
		fmt.Printf("[DARWIN-INJECT] Please enable 'Accessibility' permission for this application in System Preferences > Security & Privacy > Privacy > Accessibility\n")
	} else {
		fmt.Printf("[DARWIN-INJECT] Accessibility permissions granted\n")
	}
	return &Injector{}
}

// InjectMouseMove injects a mouse movement event
func (i *Injector) InjectMouseMove(dx, dy int) error {
	// Log the injection attempt
	fmt.Printf("[DARWIN-INJECT] Injecting mouse move: dx=%d, dy=%d\n", dx, dy)
	C.injectMouseMove(C.CGFloat(dx), C.CGFloat(dy))
	fmt.Printf("[DARWIN-INJECT] Mouse move injection completed\n")
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

// InjectKey injects a keyboard event
func (i *Injector) InjectKey(keyCode uint16, pressed bool, modifiers uint16) error {
	var cPressed C.bool
	if pressed {
		cPressed = C.bool(true)
	} else {
		cPressed = C.bool(false)
	}

	C.injectKey(C.CGKeyCode(keyCode), cPressed)
	return nil
}
