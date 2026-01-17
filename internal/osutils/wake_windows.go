//go:build windows

package osutils

import (
	"log"
	"syscall"
	"unsafe"
)

var (
	user32        = syscall.NewLazyDLL("user32.dll")
	procSendInput = user32.NewProc("SendInput")
)

const (
	INPUT_MOUSE      = 0
	MOUSEEVENTF_MOVE = 0x0001
)

type MOUSEINPUT struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type INPUT struct {
	Type uint32
	Mi   MOUSEINPUT
	_    [8]byte // Padding to match C structure alignment
}

// WakeUp simulates a small mouse movement to wake the system from sleep or screensaver
func WakeUp() {
	log.Println("WakeUp: Simulating mouse movement to wake system...")

	// Create mouse move input (relative movement of 1 pixel)
	var input INPUT
	input.Type = INPUT_MOUSE
	input.Mi.Dx = 1
	input.Mi.Dy = 1
	input.Mi.DwFlags = MOUSEEVENTF_MOVE

	// Send input
	procSendInput.Call(
		1,
		uintptr(unsafe.Pointer(&input)),
		unsafe.Sizeof(input),
	)

	// Move back
	input.Mi.Dx = -1
	input.Mi.Dy = -1
	procSendInput.Call(
		1,
		uintptr(unsafe.Pointer(&input)),
		unsafe.Sizeof(input),
	)
}
