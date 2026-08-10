//go:build darwin

package input

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices
#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>
*/
import "C"

// CurrentPermissions uses the public preflight APIs. It does not prompt or
// mutate TCC state; the UI/installer must guide the user when either is false.
func CurrentPermissions() PermissionStatus {
	return PermissionStatus{
		Accessibility:   C.AXIsProcessTrusted() != 0,
		InputMonitoring: bool(C.CGPreflightListenEventAccess()),
	}
}
