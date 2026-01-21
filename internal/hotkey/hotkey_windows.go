//go:build windows

package hotkey

import (
	"fmt"
	"log"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procSetWindowsHookEx    = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessage     = user32.NewProc("DispatchMessageW")
	procGetKeyState         = user32.NewProc("GetKeyState")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
)

const (
	WH_KEYBOARD_LL = 13
	WH_MOUSE_LL    = 14
	WM_KEYDOWN     = 0x0100
	WM_KEYUP       = 0x0101
	WM_SYSKEYDOWN  = 0x0104
	WM_SYSKEYUP    = 0x0105

	WM_LBUTTONDOWN = 0x0201
	WM_LBUTTONUP   = 0x0202
	WM_RBUTTONDOWN = 0x0204
	WM_RBUTTONUP   = 0x0205
	WM_MBUTTONDOWN = 0x0207
	WM_MBUTTONUP   = 0x0208
	WM_XBUTTONDOWN = 0x020B
	WM_XBUTTONUP   = 0x020C
)

type KBDLLHOOKSTRUCT struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type MSLLHOOKSTRUCT struct {
	Point       struct{ X, Y int32 }
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

var (
	instanceManager *Manager
	keyboardHook    uintptr
	mouseHook       uintptr
)

func (m *Manager) startPlatform() error {
	instanceManager = m

	// Hooks must be registered in the same thread that runs the message loop
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hMod, _, _ := procGetModuleHandle.Call(0)

		var err error
		keyboardHook, _, err = procSetWindowsHookEx.Call(
			WH_KEYBOARD_LL,
			syscall.NewCallback(keyboardHookPtr),
			hMod,
			0,
		)
		if keyboardHook == 0 {
			log.Printf("Error setting keyboard hook: %v", err)
			return
		}

		mouseHook, _, err = procSetWindowsHookEx.Call(
			WH_MOUSE_LL,
			syscall.NewCallback(mouseHookPtr),
			hMod,
			0,
		)
		if mouseHook == 0 {
			log.Printf("Error setting mouse hook: %v", err)
			return
		}

		log.Println("Hotkey Engine: Windows Global Hooks started.")

		var msg struct {
			Hwnd    syscall.Handle
			Message uint32
			Wparam  uintptr
			Lparam  uintptr
			Time    uint32
			Pt      struct{ X, Y int32 }
		}

		for {
			ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
		}

		procUnhookWindowsHookEx.Call(keyboardHook)
		procUnhookWindowsHookEx.Call(mouseHook)
	}()

	return nil
}

func keyboardHookPtr(nCode int, wParam uintptr, lParam uintptr) uintptr {
	if nCode == 0 {
		kbd := (*KBDLLHOOKSTRUCT)(unsafe.Pointer(lParam))
		keyName := vkCodeToName(kbd.VkCode)
		if keyName != "" {
			isDown := wParam == WM_KEYDOWN || wParam == WM_SYSKEYDOWN
			instanceManager.UpdateState(keyName, isDown)
		}
	}
	ret, _, _ := procCallNextHookEx.Call(keyboardHook, uintptr(nCode), wParam, lParam)
	return ret
}

func mouseHookPtr(nCode int, wParam uintptr, lParam uintptr) uintptr {
	if nCode == 0 {
		ms := (*MSLLHOOKSTRUCT)(unsafe.Pointer(lParam))
		var btnName string
		var isDown bool

		switch wParam {
		case WM_LBUTTONDOWN:
			btnName, isDown = "MOUSE1", true
		case WM_LBUTTONUP:
			btnName, isDown = "MOUSE1", false
		case WM_RBUTTONDOWN:
			btnName, isDown = "MOUSE3", true
		case WM_RBUTTONUP:
			btnName, isDown = "MOUSE3", false
		case WM_MBUTTONDOWN:
			btnName, isDown = "MOUSE2", true
		case WM_MBUTTONUP:
			btnName, isDown = "MOUSE2", false
		case WM_XBUTTONDOWN:
			if (ms.MouseData >> 16) == 1 {
				btnName = "MOUSE4"
			} else {
				btnName = "MOUSE5"
			}
			isDown = true
		case WM_XBUTTONUP:
			if (ms.MouseData >> 16) == 1 {
				btnName = "MOUSE4"
			} else {
				btnName = "MOUSE5"
			}
			isDown = false
		}

		if btnName != "" {
			instanceManager.UpdateState(btnName, isDown)
		}
	}
	ret, _, _ := procCallNextHookEx.Call(mouseHook, uintptr(nCode), wParam, lParam)
	return ret
}

func vkCodeToName(vk uint32) string {
	// Modifier keys
	switch vk {
	case 0x11, 0xA2, 0xA3:
		return "CTRL"
	case 0x12, 0xA4, 0xA5:
		return "ALT"
	case 0x10, 0xA0, 0xA1:
		return "SHIFT"
	case 0x5B, 0x5C:
		return "CMD" // Windows key as CMD for consistency
	case 0x20:
		return "SPACE"
	case 0x0D:
		return "ENTER"
	case 0x1B:
		return "ESC"
	case 0x08:
		return "BACKSPACE"
	case 0x09:
		return "TAB"
	case 0x14:
		return "CAPSLOCK"
	case 0x21:
		return "PAGEUP"
	case 0x22:
		return "PAGEDOWN"
	case 0x23:
		return "END"
	case 0x24:
		return "HOME"
	case 0x25:
		return "LEFT"
	case 0x26:
		return "UP"
	case 0x27:
		return "RIGHT"
	case 0x28:
		return "DOWN"
	case 0x2C:
		return "PRINTSCREEN"
	case 0x2D:
		return "INSERT"
	case 0x2E:
		return "DELETE"
	case 0x13:
		return "PAUSE"
	case 0x91:
		return "SCROLLLOCK"
	}

	// Letters A-Z
	if vk >= 0x41 && vk <= 0x5A {
		return string(rune(vk))
	}

	// Numbers 0-9
	if vk >= 0x30 && vk <= 0x39 {
		return string(rune(vk))
	}

	// F1-F12
	if vk >= 0x70 && vk <= 0x7B {
		return fmt.Sprintf("F%d", vk-0x6F)
	}

	return ""
}
