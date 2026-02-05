//go:build windows

package input

import (
	"fmt"
	"log"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Windows implementation of input capture using Raw Input API

// Trap represents a Windows input trap
type Trap struct {
	hwnd           syscall.Handle
	events         chan InputEvent
	running        bool
	mu             sync.Mutex
	killSwitch     func()
	cursorX        int
	cursorY        int
	mouseHook      syscall.Handle
	keyHook        syscall.Handle
	lastMouseX     int32
	lastMouseY     int32
	captureEnabled bool // When true, blocks input from reaching system
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
	IDI_APPLICATION        = 32512
	IDC_ARROW              = 32512
	WS_EX_TRANSPARENT      = 0x00000020
	WS_EX_LAYERED          = 0x00080000
	WS_EX_TOPMOST          = 0x00000008
	LWA_ALPHA              = 0x00000002
	WS_VISIBLE             = 0x10000000
	WS_POPUP               = 0x80000000
	WH_MOUSE_LL            = 14
	WH_KEYBOARD_LL         = 13
	WM_MOUSEMOVE           = 0x0200
	WM_LBUTTONDOWN         = 0x0201
	WM_LBUTTONUP           = 0x0202
	WM_RBUTTONDOWN         = 0x0204
	WM_RBUTTONUP           = 0x0205
	WM_MBUTTONDOWN         = 0x0207
	WM_MBUTTONUP           = 0x0208
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
	GetClientRect              = user32.NewProc("GetClientRect")
	PostQuitMessage            = user32.NewProc("PostQuitMessage")
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
		events:     make(chan InputEvent, 1000), // Increased buffer size
		cursorX:    0,
		cursorY:    0,
		lastMouseX: -1,
		lastMouseY: -1,
	}
}

// Start begins capturing input
func (t *Trap) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		return fmt.Errorf("trap already running")
	}

	// Create window for raw input
	if err := t.createWindow(); err != nil {
		return fmt.Errorf("failed to create window: %w", err)
	}

	// Register for raw input
	if err := t.registerRawInput(); err != nil {
		return fmt.Errorf("failed to register raw input: %w", err)
	}

	// Register kill switch hotkey (Ctrl+Alt+Esc)
	if err := t.registerKillSwitch(); err != nil {
		return fmt.Errorf("failed to register kill switch: %w", err)
	}

	t.running = true

	// Start message loop thread
	go t.messageLoop()

	return nil
}

// Stop stops capturing input
func (t *Trap) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.running {
		return nil
	}

	t.running = false

	// Release cursor clipping
	ClipCursor.Call(0) // NULL rect releases clipping

	// Unregister hotkey
	UnregisterHotKey.Call(uintptr(t.hwnd), 1)

	// Unhook input hooks
	if t.mouseHook != 0 {
		UnhookWindowsHookEx.Call(uintptr(t.mouseHook))
		t.mouseHook = 0
	}
	if t.keyHook != 0 {
		UnhookWindowsHookEx.Call(uintptr(t.keyHook))
		t.keyHook = 0
	}

	// Close events channel
	close(t.events)

	return nil
}

// Events returns the input event channel
func (t *Trap) Events() <-chan InputEvent {
	return t.events
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
	t.mu.Lock()
	defer t.mu.Unlock()
	t.captureEnabled = enabled
	log.Printf("Input capture %s", map[bool]string{true: "enabled", false: "disabled"}[enabled])
}

// IsCaptureEnabled returns whether capture mode is currently enabled
func (t *Trap) IsCaptureEnabled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.captureEnabled
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

	// Create a layered window for receiving raw input messages
	hwnd, _, err := CreateWindowEx.Call(
		WS_EX_LAYERED|WS_EX_TRANSPARENT, // layered and transparent
		uintptr(unsafe.Pointer(className)),
		0,          // no title
		WS_VISIBLE, // visible window
		0, 0, 1, 1, // 1x1 pixel window
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx failed: %v", err)
	}

	t.hwnd = syscall.Handle(hwnd)

	// Set window to be almost completely transparent (but visible)
	SetLayeredWindowAttributes.Call(uintptr(hwnd), 0, 1, LWA_ALPHA)

	// Try to bring window to foreground
	SetForegroundWindow.Call(uintptr(hwnd))

	return nil
}

// messageLoop runs the Windows message loop
func (t *Trap) messageLoop() {
	var msg MSG

	for t.running {
		// Use PeekMessage to check for messages without blocking
		ret, _, _ := PeekMessage.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0, 1, // PM_REMOVE = 1
		)

		if int32(ret) != 0 {
			// We have a message
			TranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			DispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
		} else {
			// No message, sleep a bit to avoid busy loop
			time.Sleep(10 * time.Millisecond)
		}
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

// setupCursorClipping sets up cursor clipping for infinite scrolling
func (t *Trap) setupCursorClipping() error {
	var rect RECT
	rect.Left = -100
	rect.Top = -100
	rect.Right = -99
	rect.Bottom = -99

	ret, _, err := ClipCursor.Call(uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return fmt.Errorf("ClipCursor failed: %v", err)
	}

	return nil
}

// windowProc handles window messages
func (t *Trap) windowProc(hwnd syscall.Handle, msg uint32, wparam uintptr, lparam uintptr) uintptr {
	switch msg {
	case WM_INPUT:
		t.handleRawInput(lparam)
		return 0
	case WM_INPUT_DEVICE_CHANGE:
		return 0
	case WM_HOTKEY:
		if t.killSwitch != nil {
			t.killSwitch()
		}
		// Also stop the trap automatically
		t.Stop()
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
		t.handleMouseInput(&rawInput.Mouse)
	} else if rawInput.Header.DwType == RIM_TYPEKEYBOARD {
		// Access keyboard data from the union
		keyboard := (*RAWKEYBOARD)(unsafe.Pointer(&rawInput.Mouse))
		t.handleKeyboardInput(keyboard)
	}
}

// handleMouseInput processes mouse input events
func (t *Trap) handleMouseInput(mouse *RAWMOUSE) {
	// Handle mouse movement (only if there's actual movement)
	if mouse.LLastX != 0 || mouse.LLastY != 0 {
		event := InputEvent{
			Type:      "mouse_move",
			DeltaX:    int(mouse.LLastX),
			DeltaY:    int(mouse.LLastY),
			Timestamp: time.Now().UnixMilli(),
		}

		// Update virtual cursor position (for relative movement)
		t.cursorX += event.DeltaX
		t.cursorY += event.DeltaY

		select {
		case t.events <- event:
		default:
			// Channel full, drop event
		}
	}

	// Handle mouse buttons (separate events)
	if mouse.UsButtonFlags&0x0001 != 0 { // RI_MOUSE_LEFT_BUTTON_DOWN
		event := InputEvent{
			Type:      "mouse_btn",
			Button:    1,
			Pressed:   true,
			Timestamp: time.Now().UnixMilli(),
		}
		select {
		case t.events <- event:
		default:
		}
	} else if mouse.UsButtonFlags&0x0002 != 0 { // RI_MOUSE_LEFT_BUTTON_UP
		event := InputEvent{
			Type:      "mouse_btn",
			Button:    1,
			Pressed:   false,
			Timestamp: time.Now().UnixMilli(),
		}
		select {
		case t.events <- event:
		default:
		}
	} else if mouse.UsButtonFlags&0x0004 != 0 { // RI_MOUSE_RIGHT_BUTTON_DOWN
		event := InputEvent{
			Type:      "mouse_btn",
			Button:    2,
			Pressed:   true,
			Timestamp: time.Now().UnixMilli(),
		}
		select {
		case t.events <- event:
		default:
		}
	} else if mouse.UsButtonFlags&0x0008 != 0 { // RI_MOUSE_RIGHT_BUTTON_UP
		event := InputEvent{
			Type:      "mouse_btn",
			Button:    2,
			Pressed:   false,
			Timestamp: time.Now().UnixMilli(),
		}
		select {
		case t.events <- event:
		default:
		}
	} else if mouse.UsButtonFlags&0x0010 != 0 { // RI_MOUSE_MIDDLE_BUTTON_DOWN
		event := InputEvent{
			Type:      "mouse_btn",
			Button:    3,
			Pressed:   true,
			Timestamp: time.Now().UnixMilli(),
		}
		select {
		case t.events <- event:
		default:
		}
	} else if mouse.UsButtonFlags&0x0020 != 0 { // RI_MOUSE_MIDDLE_BUTTON_UP
		event := InputEvent{
			Type:      "mouse_btn",
			Button:    3,
			Pressed:   false,
			Timestamp: time.Now().UnixMilli(),
		}
		select {
		case t.events <- event:
		default:
		}
	}
}

// handleKeyboardInput processes keyboard input events
func (t *Trap) handleKeyboardInput(keyboard *RAWKEYBOARD) {
	event := InputEvent{
		Type:      "key",
		KeyCode:   uint16(keyboard.VKey),
		Timestamp: time.Now().UnixMilli(),
	}

	// Check if key is pressed or released
	if keyboard.Flags&0x01 != 0 { // RI_KEY_BREAK
		event.Pressed = false
	} else {
		event.Pressed = true
	}

	select {
	case t.events <- event:
	default:
	}
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
	if nCode >= 0 {
		hookStruct := (*MSLLHOOKSTRUCT)(unsafe.Pointer(lParam))
		msg := uint32(wParam)

		event := InputEvent{
			Type:      "mouse_move",
			DeltaX:    0,
			DeltaY:    0,
			Timestamp: time.Now().UnixMilli(),
		}

		switch msg {
		case WM_MOUSEMOVE:
			event.Type = "mouse_move"
			// Calculate relative movement from last position
			if t.lastMouseX != -1 && t.lastMouseY != -1 {
				event.DeltaX = int(hookStruct.Pt.X - t.lastMouseX)
				event.DeltaY = int(hookStruct.Pt.Y - t.lastMouseY)
			} else {
				// First mouse move, just initialize position without sending event
				event.DeltaX = 0
				event.DeltaY = 0
			}
			// Update last position
			t.lastMouseX = hookStruct.Pt.X
			t.lastMouseY = hookStruct.Pt.Y
		case WM_LBUTTONDOWN:
			event.Type = "mouse_btn"
			event.Button = 1
			event.Pressed = true
		case WM_LBUTTONUP:
			event.Type = "mouse_btn"
			event.Button = 1
			event.Pressed = false
		case WM_RBUTTONDOWN:
			event.Type = "mouse_btn"
			event.Button = 2
			event.Pressed = true
		case WM_RBUTTONUP:
			event.Type = "mouse_btn"
			event.Button = 2
			event.Pressed = false
		case WM_MBUTTONDOWN:
			event.Type = "mouse_btn"
			event.Button = 3
			event.Pressed = true
		case WM_MBUTTONUP:
			event.Type = "mouse_btn"
			event.Button = 3
			event.Pressed = false
		}

		select {
		case t.events <- event:
		default:
			// Channel full, drop event
		}

		// If capture mode is enabled, block input from reaching system
		t.mu.Lock()
		captureEnabled := t.captureEnabled
		t.mu.Unlock()

		if captureEnabled {
			// Return 1 to block input
			return 1
		}
	}

	ret, _, _ := CallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return ret
}

// keyboardHookProc handles keyboard hook events
func (t *Trap) keyboardHookProc(nCode int32, wParam uintptr, lParam uintptr) uintptr {
	if nCode >= 0 {
		hookStruct := (*KBDLLHOOKSTRUCT)(unsafe.Pointer(lParam))
		msg := uint32(wParam)

		event := InputEvent{
			Type:      "key",
			KeyCode:   uint16(hookStruct.VkCode),
			Timestamp: time.Now().UnixMilli(),
		}

		if msg == 0x0100 { // WM_KEYDOWN
			event.Pressed = true
		} else if msg == 0x0101 { // WM_KEYUP
			event.Pressed = false
		}

		// Don't log anything to avoid blocking

		select {
		case t.events <- event:
		default:
			// Channel full, drop event
		}

		// If capture mode is enabled, block input from reaching system
		// Exception: Let hotkeys registered with RegisterHotKey still work
		t.mu.Lock()
		captureEnabled := t.captureEnabled
		t.mu.Unlock()

		if captureEnabled {
			// Return 1 to block input
			// Note: RegisterHotKey hotkeys work at a lower level and will still function
			return 1
		}
	}

	ret, _, _ := CallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
	return ret
}
