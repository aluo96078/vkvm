//go:build windows

package input

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Windows implementation of input capture using Raw Input API

// Trap represents a Windows input trap
type Trap struct {
	hwnd           syscall.Handle
	eventBuffer    *EventBuffer
	running        bool
	mu             sync.Mutex
	killSwitch     func()
	cursorX        int
	cursorY        int
	mouseHook      syscall.Handle
	keyHook        syscall.Handle
	lastMouseX     int32
	lastMouseY     int32
	captureEnabled int32 // atomic: 1=enabled, 0=disabled
	runningState   int32
	threadReady    chan error
	threadDone     chan struct{}
	pressedScans   map[uint16]struct{}
}

// Windows API constants and types
const (
	WM_INPUT               = 0x00FF
	WM_INPUT_DEVICE_CHANGE = 0x00FE
	WM_HOTKEY              = 0x0312
	RIM_TYPEMOUSE          = 0
	RIM_TYPEKEYBOARD       = 1
	RID_INPUT              = 0x10000003
	RIDEV_INPUTSINK        = 0x00000100
	RIDEV_NOLEGACY         = 0x00000030
	RIDEV_CAPTUREMOUSE     = 0x00000200
	MOD_CONTROL            = 0x0002
	MOD_ALT                = 0x0001
	VK_ESCAPE              = 0x1B
	VK_LSHIFT              = 0xA0
	VK_RSHIFT              = 0xA1
	VK_LCONTROL            = 0xA2
	VK_RCONTROL            = 0xA3
	VK_LMENU               = 0xA4
	VK_RMENU               = 0xA5
	VK_LWIN                = 0x5B
	VK_RWIN                = 0x5C
	IDI_APPLICATION        = 32512
	IDC_ARROW              = 32512
	WS_EX_TRANSPARENT      = 0x00000020
	WS_EX_LAYERED          = 0x00080000
	WS_EX_TOPMOST          = 0x00000008
	WS_EX_TOOLWINDOW       = 0x00000080
	WS_EX_NOACTIVATE       = 0x08000000
	LWA_ALPHA              = 0x00000002
	WS_VISIBLE             = 0x10000000
	WS_POPUP               = 0x80000000
	SW_HIDE                = 0
	WH_MOUSE_LL            = 14
	WH_KEYBOARD_LL         = 13
	WM_MOUSEMOVE           = 0x0200
	WM_LBUTTONDOWN         = 0x0201
	WM_LBUTTONUP           = 0x0202
	WM_RBUTTONDOWN         = 0x0204
	WM_RBUTTONUP           = 0x0205
	WM_MBUTTONDOWN         = 0x0207
	WM_MBUTTONUP           = 0x0208
	WM_MOUSEWHEEL          = 0x020A
	WM_MOUSEHWHEEL         = 0x020E
	WM_XBUTTONDOWN         = 0x020B
	WM_XBUTTONUP           = 0x020C
	WM_CLOSE               = 0x0010
	XBUTTON1               = 0x0001
	XBUTTON2               = 0x0002
	CW_USEDEFAULT          = 0x80000000
	SPI_GETWORKAREA        = 0x0030
)

// Windows API functions
var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	RegisterRawInputDevices    = user32.NewProc("RegisterRawInputDevices")
	GetRawInputData            = user32.NewProc("GetRawInputData")
	CreateWindowEx             = user32.NewProc("CreateWindowExW")
	DefWindowProc              = user32.NewProc("DefWindowProcW")
	RegisterClassEx            = user32.NewProc("RegisterClassExW")
	GetMessage                 = user32.NewProc("GetMessageW")
	PeekMessage                = user32.NewProc("PeekMessageW")
	MsgWaitForMultipleObjects  = user32.NewProc("MsgWaitForMultipleObjects")
	TranslateMessage           = user32.NewProc("TranslateMessage")
	DispatchMessage            = user32.NewProc("DispatchMessageW")
	RegisterHotKey             = user32.NewProc("RegisterHotKey")
	UnregisterHotKey           = user32.NewProc("UnregisterHotKey")
	ClipCursor                 = user32.NewProc("ClipCursor")
	GetCursorPos               = user32.NewProc("GetCursorPos")
	SetCursorPos               = user32.NewProc("SetCursorPos")
	SetCursor                  = user32.NewProc("SetCursor")
	LoadCursor                 = user32.NewProc("LoadCursorW")
	LoadIcon                   = user32.NewProc("LoadIconW")
	GetWindowRect              = user32.NewProc("GetWindowRect")
	ShowWindow                 = user32.NewProc("ShowWindow")
	UpdateWindow               = user32.NewProc("UpdateWindow")
	SetWindowPos               = user32.NewProc("SetWindowPos")
	SetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	SetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	SetWindowsHookEx           = user32.NewProc("SetWindowsHookExW")
	UnhookWindowsHookEx        = user32.NewProc("UnhookWindowsHookEx")
	CallNextHookEx             = user32.NewProc("CallNextHookEx")
	GetKeyState                = user32.NewProc("GetKeyState")
	GetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	GetClientRect              = user32.NewProc("GetClientRect")
	PostQuitMessage            = user32.NewProc("PostQuitMessage")
	PostMessage                = user32.NewProc("PostMessageW")
	DestroyWindow              = user32.NewProc("DestroyWindow")
	SystemParametersInfo       = user32.NewProc("SystemParametersInfoW")
	GetModuleHandle            = kernel32.NewProc("GetModuleHandleW")
)

// Windows API structures
type WNDCLASSEX struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type MSG struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}

type POINT struct {
	X, Y int32
}

type RECT struct {
	Left, Top, Right, Bottom int32
}

type RAWINPUTDEVICE struct {
	UsUsagePage uint16
	UsUsage     uint16
	DwFlags     uint32
	HwndTarget  syscall.Handle
}

type RAWINPUTHEADER struct {
	DwType  uint32
	DwSize  uint32
	HDevice syscall.Handle
	WParam  uintptr
}

type RAWMOUSE struct {
	UsFlags            uint16
	_                  uint16 // padding for alignment
	UsButtonFlags      uint16 // union: can also be accessed as ulButtons (uint32)
	UsButtonData       uint16 // union: part of ulButtons
	UlRawButtons       uint32
	LLastX             int32
	LLastY             int32
	UlExtraInformation uint32
}

type RAWKEYBOARD struct {
	MakeCode         uint16
	Flags            uint16
	Reserved         uint16
	VKey             uint16
	Message          uint32
	ExtraInformation uint32
}

type MSLLHOOKSTRUCT struct {
	Pt          POINT
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type KBDLLHOOKSTRUCT struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type RAWINPUT struct {
	Header RAWINPUTHEADER
	Mouse  RAWMOUSE
	// Note: Union in C, but we access via pointer
}

// NewTrap creates a new input trap for Windows
func NewTrap() *Trap {
	return &Trap{
		eventBuffer:  NewEventBuffer(4096),
		cursorX:      0,
		cursorY:      0,
		lastMouseX:   -1,
		lastMouseY:   -1,
		pressedScans: make(map[uint16]struct{}),
	}
}

// Start begins capturing input
func (t *Trap) Start() error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return fmt.Errorf("trap already running")
	}
	t.running = true
	atomic.StoreInt32(&t.runningState, 1)
	if t.eventBuffer != nil {
		t.eventBuffer.Close()
	}
	t.eventBuffer = NewEventBuffer(4096)
	t.pressedScans = make(map[uint16]struct{})
	t.threadReady = make(chan error, 1)
	t.threadDone = make(chan struct{})
	ready := t.threadReady
	go t.captureThread()
	t.mu.Unlock()

	if err := <-ready; err != nil {
		t.mu.Lock()
		t.running = false
		atomic.StoreInt32(&t.runningState, 0)
		t.mu.Unlock()
		return err
	}

	return nil
}

// Stop stops capturing input
func (t *Trap) Stop() error {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return nil
	}
	hwnd := t.hwnd
	done := t.threadDone
	t.mu.Unlock()

	atomic.StoreInt32(&t.captureEnabled, 0)
	if hwnd != 0 {
		PostMessage.Call(uintptr(hwnd), WM_CLOSE, 0, 0)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			return fmt.Errorf("timed out waiting for input thread to stop")
		}
	}
	return nil
}

// Events returns the input event channel
func (t *Trap) Events() <-chan InputEvent {
	t.mu.Lock()
	buffer := t.eventBuffer
	t.mu.Unlock()
	if buffer == nil {
		return nil
	}
	return buffer.Events()
}

// SetKillSwitch registers Ctrl+Alt+Esc to release control
func (t *Trap) SetKillSwitch(callback func()) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.killSwitch = callback
	return nil
}

// EnableCapture enables or disables input capture mode
// When enabled, input is blocked from reaching the system
func (t *Trap) EnableCapture(enabled bool) {
	if enabled {
		atomic.StoreInt32(&t.captureEnabled, 1)
	} else {
		atomic.StoreInt32(&t.captureEnabled, 0)
	}

	if enabled {
		// Move cursor to center of screen when enabling capture
		screenWidth, _, _ := GetSystemMetrics.Call(0)  // SM_CXSCREEN
		screenHeight, _, _ := GetSystemMetrics.Call(1) // SM_CYSCREEN
		centerX := screenWidth / 2
		centerY := screenHeight / 2
		SetCursorPos.Call(centerX, centerY)
	}

	log.Printf("Input capture %s", map[bool]string{true: "enabled", false: "disabled"}[enabled])
}

// IsCaptureEnabled returns whether capture mode is currently enabled
func (t *Trap) IsCaptureEnabled() bool {
	return atomic.LoadInt32(&t.captureEnabled) == 1
}

// createWindow creates a transparent overlay window
func (t *Trap) createWindow() error {
	className := syscall.StringToUTF16Ptr("VKVMInputTrap")

	// Register window class
	hInstance, _, _ := GetModuleHandle.Call(0)
	wndClass := WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(t.windowProc),
		HInstance:     syscall.Handle(hInstance),
		HIcon:         0, // Default icon
		HCursor:       0, // Default cursor
		LpszClassName: className,
	}

	ret, _, err := RegisterClassEx.Call(uintptr(unsafe.Pointer(&wndClass)))
	if ret == 0 {
		return fmt.Errorf("RegisterClassEx failed: %v", err)
	}

	// Get screen dimensions
	var rect RECT
	SystemParametersInfo.Call(uintptr(SPI_GETWORKAREA), 0, uintptr(unsafe.Pointer(&rect)), 0)

	// Create a hidden message-only window for hotkey registration and hook hosting
	// WS_EX_TOOLWINDOW: no taskbar button
	// WS_EX_NOACTIVATE: does not steal focus
	hwnd, _, err := CreateWindowEx.Call(
		WS_EX_TOOLWINDOW|WS_EX_NOACTIVATE,
		uintptr(unsafe.Pointer(className)),
		0,          // no title
		WS_POPUP,   // no visible decoration, not shown
		0, 0, 0, 0, // zero size
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx failed: %v", err)
	}

	t.hwnd = syscall.Handle(hwnd)

	// Ensure the window stays hidden
	ShowWindow.Call(uintptr(hwnd), SW_HIDE)

	return nil
}

// captureThread owns all Windows input resources and their message queue on
// one locked OS thread. The caller waits for threadReady before forwarding
// events, so a partially initialized trap cannot be used.
func (t *Trap) captureThread() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.threadDone)

	if err := t.createWindow(); err != nil {
		t.threadReady <- fmt.Errorf("failed to create window: %w", err)
		return
	}
	if err := t.registerKillSwitch(); err != nil {
		t.threadReady <- fmt.Errorf("failed to register kill switch: %w", err)
		t.cleanupThread()
		return
	}
	if err := t.registerRawInput(); err != nil {
		t.threadReady <- fmt.Errorf("failed to register raw input: %w", err)
		t.cleanupThread()
		return
	}
	if err := t.installHooks(); err != nil {
		t.threadReady <- fmt.Errorf("failed to install hooks: %w", err)
		t.cleanupThread()
		return
	}
	t.threadReady <- nil

	var msg MSG
	for atomic.LoadInt32(&t.runningState) == 1 {
		ret, _, _ := GetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		TranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		DispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	t.cleanupThread()
}

func (t *Trap) cleanupThread() {
	atomic.StoreInt32(&t.runningState, 0)
	ClipCursor.Call(0)
	if t.hwnd != 0 {
		UnregisterHotKey.Call(uintptr(t.hwnd), 1)
	}
	if t.mouseHook != 0 {
		UnhookWindowsHookEx.Call(uintptr(t.mouseHook))
		t.mouseHook = 0
	}
	if t.keyHook != 0 {
		UnhookWindowsHookEx.Call(uintptr(t.keyHook))
		t.keyHook = 0
	}
	t.mu.Lock()
	t.running = false
	hwnd := t.hwnd
	t.hwnd = 0
	if t.eventBuffer != nil {
		t.eventBuffer.Close()
	}
	t.mu.Unlock()
	if hwnd != 0 {
		DestroyWindow.Call(uintptr(hwnd))
	}
}

// registerRawInput registers for raw mouse input
func (t *Trap) registerRawInput() error {
	rids := []RAWINPUTDEVICE{
		{
			UsUsagePage: 0x01, // HID_USAGE_PAGE_GENERIC
			UsUsage:     0x02, // HID_USAGE_GENERIC_MOUSE
			DwFlags:     RIDEV_INPUTSINK,
			HwndTarget:  t.hwnd,
		},
		{
			UsUsagePage: 0x01, // HID_USAGE_PAGE_GENERIC
			UsUsage:     0x06, // HID_USAGE_GENERIC_KEYBOARD
			DwFlags:     RIDEV_INPUTSINK,
			HwndTarget:  t.hwnd,
		},
	}

	for i, rid := range rids {
		ret, _, err := RegisterRawInputDevices.Call(
			uintptr(unsafe.Pointer(&rids[i])),
			1,
			uintptr(unsafe.Sizeof(rid)),
		)
		if ret == 0 {
			return fmt.Errorf("RegisterRawInputDevices failed for device %d: %v", i, err)
		}
	}

	return nil
}

// registerKillSwitch registers the Ctrl+Alt+Esc hotkey
func (t *Trap) registerKillSwitch() error {
	// Try to register Ctrl+Alt+Esc first
	ret, _, err := RegisterHotKey.Call(
		uintptr(t.hwnd),
		1, // hotkey ID
		MOD_CONTROL|MOD_ALT,
		VK_ESCAPE,
	)
	if ret != 0 {
		return nil // Success
	}

	// Check if it's the "already registered" error
	if errno, ok := err.(syscall.Errno); ok && errno == 1409 { // ERROR_HOTKEY_ALREADY_REGISTERED
		// Try alternative hotkeys
		alternatives := []struct {
			modifiers uint32
			key       uint32
			desc      string
		}{
			{MOD_CONTROL | MOD_ALT, 0x51, "Ctrl+Alt+Q"}, // Q key
			{MOD_CONTROL | MOD_ALT, 0x57, "Ctrl+Alt+W"}, // W key
			{MOD_CONTROL, VK_ESCAPE, "Ctrl+Esc"},        // Just Ctrl+Esc
		}

		for _, alt := range alternatives {
			ret, _, err = RegisterHotKey.Call(
				uintptr(t.hwnd),
				1, // hotkey ID
				uintptr(alt.modifiers),
				uintptr(alt.key),
			)
			if ret != 0 {
				return nil
			}
		}

		return fmt.Errorf("RegisterHotKey failed: All hotkey combinations are already registered. " +
			"Please close other applications that might be using Ctrl+Alt+Esc, Ctrl+Alt+Q, Ctrl+Alt+W, or Ctrl+Esc")
	}

	return fmt.Errorf("RegisterHotKey failed: %v", err)
}

// installHooks installs low-level mouse and keyboard hooks for input blocking
func (t *Trap) installHooks() error {
	// Install mouse hook
	mouseHook, _, err := SetWindowsHookEx.Call(
		WH_MOUSE_LL,
		syscall.NewCallback(t.mouseHookProc),
		0, // hMod (NULL for low-level hooks)
		0, // dwThreadId (0 for global hooks)
	)
	if mouseHook == 0 {
		return fmt.Errorf("failed to install mouse hook: %v", err)
	}
	t.mouseHook = syscall.Handle(mouseHook)

	// Install keyboard hook
	keyHook, _, err := SetWindowsHookEx.Call(
		WH_KEYBOARD_LL,
		syscall.NewCallback(t.keyboardHookProc),
		0, // hMod (NULL for low-level hooks)
		0, // dwThreadId (0 for global hooks)
	)
	if keyHook == 0 {
		return fmt.Errorf("failed to install keyboard hook: %v", err)
	}
	t.keyHook = syscall.Handle(keyHook)

	return nil
}

// getKeyState gets the state of a virtual key
func getKeyState(vk uint32) int16 {
	ret, _, _ := GetKeyState.Call(uintptr(vk))
	return int16(ret)
}

// windowProc handles window messages
func (t *Trap) windowProc(hwnd syscall.Handle, msg uint32, wparam uintptr, lparam uintptr) uintptr {
	switch msg {
	case WM_CLOSE:
		PostQuitMessage.Call(0)
		return 0
	case WM_INPUT:
		t.handleRawInput(lparam)
		return 0
	case WM_INPUT_DEVICE_CHANGE:
		return 0
	case WM_HOTKEY:
		t.mu.Lock()
		callback := t.killSwitch
		t.mu.Unlock()
		if callback != nil {
			// Stop waits for this message loop to exit; never call it inline
			// from the window procedure.
			go callback()
		} else {
			PostMessage.Call(uintptr(hwnd), WM_CLOSE, 0, 0)
		}
		return 0
	}

	ret, _, _ := DefWindowProc.Call(
		uintptr(hwnd),
		uintptr(msg),
		wparam,
		lparam,
	)
	return ret
}

// handleRawInput processes raw input data
func (t *Trap) handleRawInput(lparam uintptr) {
	// Raw Input is the event source for forwarding. Do not queue events while
	// capture is disabled; the low-level hooks below only block input then.
	if atomic.LoadInt32(&t.captureEnabled) != 1 {
		return
	}
	var size uint32 = 0

	// Get the size of the raw input data (first call with NULL data pointer)
	ret, _, _ := GetRawInputData.Call(
		lparam,
		RID_INPUT,
		0, // NULL data pointer
		uintptr(unsafe.Pointer(&size)),
		unsafe.Sizeof(RAWINPUTHEADER{}),
	)

	if ret == 0xFFFFFFFF || size == 0 {
		return
	}

	// Allocate buffer for raw input data
	data := make([]byte, size)
	ret, _, _ = GetRawInputData.Call(
		lparam,
		RID_INPUT,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(unsafe.Pointer(&size)),
		unsafe.Sizeof(RAWINPUTHEADER{}),
	)

	if ret == 0xFFFFFFFF || ret == 0 {
		return
	}

	// Parse the raw input data
	rawInput := (*RAWINPUT)(unsafe.Pointer(&data[0]))

	if rawInput.Header.DwType == RIM_TYPEMOUSE {
		t.handleMouseInput(&rawInput.Mouse, rawHardwareID(rawInput.Header.HDevice))
	} else if rawInput.Header.DwType == RIM_TYPEKEYBOARD {
		// Access keyboard data from the union
		keyboard := (*RAWKEYBOARD)(unsafe.Pointer(&rawInput.Mouse))
		t.handleKeyboardInput(keyboard, rawHardwareID(rawInput.Header.HDevice))
	}
}

func rawHardwareID(device syscall.Handle) string {
	if device == 0 {
		return "hid-default"
	}
	return fmt.Sprintf("hid-%x", uintptr(device))
}

// handleMouseInput processes mouse input events
func (t *Trap) handleMouseInput(mouse *RAWMOUSE, hardwareID string) {
	// Handle mouse movement (only if there's actual movement)
	if mouse.LLastX != 0 || mouse.LLastY != 0 {
		event := InputEvent{
			Type:       "mouse_move",
			DeltaX:     int(mouse.LLastX),
			DeltaY:     int(mouse.LLastY),
			Timestamp:  time.Now().UnixMilli(),
			ClockNS:    time.Now().UnixNano(),
			HardwareID: hardwareID,
		}

		// Update virtual cursor position (for relative movement)
		t.cursorX += event.DeltaX
		t.cursorY += event.DeltaY

		t.enqueueEvent(event, false)
	}

	// Handle mouse buttons (separate events)
	if mouse.UsButtonFlags&0x0001 != 0 { // RI_MOUSE_LEFT_BUTTON_DOWN
		t.enqueueMouseButton(1, true, hardwareID)
	}
	if mouse.UsButtonFlags&0x0002 != 0 {
		t.enqueueMouseButton(1, false, hardwareID)
	}
	if mouse.UsButtonFlags&0x0004 != 0 {
		t.enqueueMouseButton(2, true, hardwareID)
	}
	if mouse.UsButtonFlags&0x0008 != 0 {
		t.enqueueMouseButton(2, false, hardwareID)
	}
	if mouse.UsButtonFlags&0x0010 != 0 {
		t.enqueueMouseButton(3, true, hardwareID)
	}
	if mouse.UsButtonFlags&0x0020 != 0 {
		t.enqueueMouseButton(3, false, hardwareID)
	}
	if mouse.UsButtonFlags&0x0040 != 0 {
		t.enqueueMouseButton(4, true, hardwareID)
	}
	if mouse.UsButtonFlags&0x0080 != 0 {
		t.enqueueMouseButton(4, false, hardwareID)
	}
	if mouse.UsButtonFlags&0x0100 != 0 {
		t.enqueueMouseButton(5, true, hardwareID)
	}
	if mouse.UsButtonFlags&0x0200 != 0 {
		t.enqueueMouseButton(5, false, hardwareID)
	}

	if mouse.UsButtonFlags&0x0400 != 0 {
		t.enqueueEvent(InputEvent{Type: "mouse_wheel", WheelDelta: int(int16(mouse.UsButtonData)), Timestamp: time.Now().UnixMilli(), ClockNS: time.Now().UnixNano(), HardwareID: hardwareID}, true)
	}
	if mouse.UsButtonFlags&0x0800 != 0 {
		t.enqueueEvent(InputEvent{Type: "mouse_wheel_h", WheelDelta: int(int16(mouse.UsButtonData)), Timestamp: time.Now().UnixMilli(), ClockNS: time.Now().UnixNano(), HardwareID: hardwareID}, true)
	}
}

func (t *Trap) enqueueMouseButton(button int, pressed bool, hardwareID string) {
	t.enqueueEvent(InputEvent{Type: "mouse_btn", Button: button, Pressed: pressed, Timestamp: time.Now().UnixMilli(), ClockNS: time.Now().UnixNano(), HardwareID: hardwareID}, true)
}

func (t *Trap) enqueueEvent(event InputEvent, critical bool) {
	t.mu.Lock()
	buffer := t.eventBuffer
	t.mu.Unlock()
	if buffer == nil || !buffer.Push(event, critical) {
		if critical {
			log.Printf("Input trap: critical event buffer closed")
		}
	}
}

// handleKeyboardInput processes keyboard input events
func (t *Trap) handleKeyboardInput(keyboard *RAWKEYBOARD, hardwareID string) {
	event := InputEvent{
		Type:       "key",
		KeyCode:    uint16(keyboard.VKey),
		Timestamp:  time.Now().UnixMilli(),
		ClockNS:    time.Now().UnixNano(),
		ScanCode:   keyboard.MakeCode,
		Extended:   keyboard.Flags&0x02 != 0,
		E1:         keyboard.Flags&0x04 != 0,
		HardwareID: hardwareID,
	}

	// Check if key is pressed or released
	if keyboard.Flags&0x01 != 0 { // RI_KEY_BREAK
		event.Pressed = false
		delete(t.pressedScans, keyboard.MakeCode)
	} else {
		event.Pressed = true
		if _, exists := t.pressedScans[keyboard.MakeCode]; exists {
			event.Repeat = true
		}
		t.pressedScans[keyboard.MakeCode] = struct{}{}
	}
	if getKeyState(VK_LSHIFT) < 0 || getKeyState(VK_RSHIFT) < 0 {
		event.Modifiers |= 0x0001
	}
	if getKeyState(VK_LCONTROL) < 0 || getKeyState(VK_RCONTROL) < 0 {
		event.Modifiers |= 0x0002
	}
	if getKeyState(VK_LMENU) < 0 || getKeyState(VK_RMENU) < 0 {
		event.Modifiers |= 0x0004
	}
	if getKeyState(VK_LWIN) < 0 || getKeyState(VK_RWIN) < 0 {
		event.Modifiers |= 0x0008
	}
	t.enqueueEvent(event, true)
}

// setupHooks sets up low-level mouse and keyboard hooks
func (t *Trap) setupHooks() error {
	// Set up mouse hook
	mouseHook, _, err := SetWindowsHookEx.Call(
		WH_MOUSE_LL,
		syscall.NewCallback(t.mouseHookProc),
		0, // hInstance
		0, // dwThreadId (0 = all threads)
	)
	if mouseHook == 0 {
		return fmt.Errorf("failed to set mouse hook: %v", err)
	}
	t.mouseHook = syscall.Handle(mouseHook)

	// Set up keyboard hook
	keyHook, _, err := SetWindowsHookEx.Call(
		WH_KEYBOARD_LL,
		syscall.NewCallback(t.keyboardHookProc),
		0, // hInstance
		0, // dwThreadId (0 = all threads)
	)
	if keyHook == 0 {
		// Clean up mouse hook
		UnhookWindowsHookEx.Call(mouseHook)
		t.mouseHook = 0
		return fmt.Errorf("failed to set keyboard hook: %v", err)
	}
	t.keyHook = syscall.Handle(keyHook)

	return nil
}

// hookThread runs hooks in a dedicated thread with message loop
func (t *Trap) hookThread() {
	// Set up input hooks in this thread
	if err := t.setupHooks(); err != nil {
		return
	}

	// Run message loop to process hooks
	var msg MSG
	for t.running {
		ret, _, _ := GetMessage.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0,
		)

		if int32(ret) <= 0 {
			break
		}

		TranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		DispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// mouseHookProc handles mouse hook events
func (t *Trap) mouseHookProc(nCode int32, wParam uintptr, lParam uintptr) uintptr {
	if nCode >= 0 && atomic.LoadInt32(&t.captureEnabled) == 1 {
		// Raw Input supplies the event payload. The low-level hook is solely a
		// fail-safe blocker so Windows acceleration and cursor recentering do not
		// alter the forwarded relative movement.
		return 1
	}
	ret, _, _ := CallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return ret
}

// keyboardHookProc handles keyboard hook events
func (t *Trap) keyboardHookProc(nCode int32, wParam uintptr, lParam uintptr) uintptr {
	if nCode >= 0 && atomic.LoadInt32(&t.captureEnabled) == 1 {
		// Raw Input supplies VKey/scan code and key-up state; the hook only
		// blocks the event while capture is active.
		return 1
	}
	ret, _, _ := CallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return ret
}
