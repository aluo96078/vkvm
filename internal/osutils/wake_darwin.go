//go:build darwin

package osutils

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>

void wakeUpMouse() {
    // Get current mouse position
    CGEventRef event = CGEventCreate(NULL);
    CGPoint loc = CGEventGetLocation(event);
    CFRelease(event);

    // Move mouse slightly (1 pixel) and back to wake up the system
    CGEventRef move1 = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved,
        CGPointMake(loc.x + 1, loc.y + 1), kCGMouseButtonLeft);
    CGEventPost(kCGHIDEventTap, move1);
    CFRelease(move1);

    CGEventRef move2 = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved,
        CGPointMake(loc.x, loc.y), kCGMouseButtonLeft);
    CGEventPost(kCGHIDEventTap, move2);
    CFRelease(move2);
}
*/
import "C"

import "log"

// WakeUp simulates a small mouse movement to wake the system from sleep or screensaver
func WakeUp() {
	log.Println("WakeUp: Simulating mouse movement to wake system...")
	C.wakeUpMouse()
}
