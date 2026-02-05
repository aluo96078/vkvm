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
	hwnd       syscall.Handle
	events     chan InputEvent
	running    bool
	mu         sync.Mutex
	killSwitch func()
	cursorX    int
	cursorY    int
}

// Windows API constants and types
const (
	WM_INPUT          = 0x00FF
	WM_HOTKEY         = 0x0312
	RIM_TYPEMOUSE     = 0
	RID_INPUT         = 0x10000003
	RIDEV_INPUTSINK   = 0x00000100
	MOD_CONTROL       = 0x0002
	MOD_ALT           = 0x0001
	VK_ESCAPE         = 0x1B
	IDI_APPLICATION   = 32512
	IDC_ARROW         = 32512
	WS_EX_TRANSPARENT = 0x00000020
	WS_EX_LAYERED     = 0x00080000
	WS_EX_TOPMOST     = 0x00000008
	WS_POPUP          = 0x80000000
	CW_USEDEFAULT     = 0x80000000
	SPI_GETWORKAREA   = 0x0030
)

// Windows API functions
var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	RegisterRawInputDevices = user32.NewProc("RegisterRawInputDevices")
	GetRawInputData         = user32.NewProc("GetRawInputData")
	CreateWindowEx          = user32.NewProc("CreateWindowExW")
	DefWindowProc           = user32.NewProc("DefWindowProcW")
	RegisterClassEx         = user32.NewProc("RegisterClassExW")
	GetMessage              = user32.NewProc("GetMessageW")
	TranslateMessage        = user32.NewProc("TranslateMessage")
	DispatchMessage         = user32.NewProc("DispatchMessageW")
	RegisterHotKey          = user32.NewProc("RegisterHotKey")
	UnregisterHotKey        = user32.NewProc("UnregisterHotKey")
	ClipCursor              = user32.NewProc("ClipCursor")
	GetCursorPos            = user32.NewProc("GetCursorPos")
	SetCursorPos            = user32.NewProc("SetCursorPos")
	SetCursor               = user32.NewProc("SetCursor")
	LoadCursor              = user32.NewProc("LoadCursorW")
	LoadIcon                = user32.NewProc("LoadIconW")
	GetWindowRect           = user32.NewProc("GetWindowRect")
	ShowWindow              = user32.NewProc("ShowWindow")
	UpdateWindow            = user32.NewProc("UpdateWindow")
	SetWindowPos            = user32.NewProc("SetWindowPos")
	GetClientRect           = user32.NewProc("GetClientRect")
	PostQuitMessage         = user32.NewProc("PostQuitMessage")
	SystemParametersInfo    = user32.NewProc("SystemParametersInfoW")
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
	UlButtons          uint32
	UsButtonFlags      uint16
	UsButtonData       uint16
	UlRawButtons       uint32
	LLastX             int32
	LLastY             int32
	UlExtraInformation uint32
}

type RAWINPUT struct {
	Header RAWINPUTHEADER
	Mouse  RAWMOUSE
}

// NewTrap creates a new input trap for Windows
func NewTrap() *Trap {
	return &Trap{
		events:  make(chan InputEvent, 100),
		cursorX: 0,
		cursorY: 0,
	}
}

// Start begins capturing input
func (t *Trap) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		return fmt.Errorf("trap already running")
	}

	// Create transparent window
	if err := t.createWindow(); err != nil {
		return fmt.Errorf("failed to create window: %w", err)
	}

	// Register Raw Input devices
	if err := t.registerRawInput(); err != nil {
		return fmt.Errorf("failed to register raw input: %w", err)
	}

	// Register kill switch hotkey (Ctrl+Alt+Esc)
	if err := t.registerKillSwitch(); err != nil {
		return fmt.Errorf("failed to register kill switch: %w", err)
	}

	// Set up cursor clipping for infinite scrolling
	if err := t.setupCursorClipping(); err != nil {
		return fmt.Errorf("failed to setup cursor clipping: %w", err)
	}

	t.running = true

	// Start message loop in a goroutine
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

// createWindow creates a transparent overlay window
func (t *Trap) createWindow() error {
	log.Printf("[DEBUG] Starting window creation process")

	className := syscall.StringToUTF16Ptr("VKVMInputTrap")
	log.Printf("[DEBUG] Window class name: %s", "VKVMInputTrap")

	// Register window class
	wndClass := WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(t.windowProc),
		HInstance:     0, // Will be set by Windows
		HIcon:         0, // Default icon
		HCursor:       0, // Default cursor
		LpszClassName: className,
	}

	log.Printf("[DEBUG] Registering window class")
	ret, _, err := RegisterClassEx.Call(uintptr(unsafe.Pointer(&wndClass)))
	if ret == 0 {
		log.Printf("[ERROR] RegisterClassEx failed with error: %v", err)
		return fmt.Errorf("RegisterClassEx failed: %v", err)
	}
	log.Printf("[DEBUG] Window class registered successfully")

	// Get screen dimensions
	var rect RECT
	log.Printf("[DEBUG] Getting screen dimensions")
	SystemParametersInfo.Call(uintptr(SPI_GETWORKAREA), 0, uintptr(unsafe.Pointer(&rect)), 0)
	screenWidth := rect.Right - rect.Left
	screenHeight := rect.Bottom - rect.Top

	log.Printf("[DEBUG] Screen dimensions obtained: %dx%d", screenWidth, screenHeight)

	// Create window covering the entire screen
	log.Printf("[DEBUG] Creating window with dimensions %dx%d", screenWidth, screenHeight)
	hwnd, _, err := CreateWindowEx.Call(
		0, // no extended styles - remove WS_EX_LAYERED and WS_EX_TRANSPARENT
		uintptr(unsafe.Pointer(className)),
		0, // no title
		WS_POPUP,
		0, 0, uintptr(screenWidth), uintptr(screenHeight), // cover entire screen
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		log.Printf("[ERROR] CreateWindowEx failed with error: %v", err)
		return fmt.Errorf("CreateWindowEx failed: %v", err)
	}

	t.hwnd = syscall.Handle(hwnd)
	log.Printf("[DEBUG] Window created with HWND: %d", hwnd)

	// Show the window (SW_SHOWNOACTIVATE to show without activating)
	log.Printf("[DEBUG] Showing window (SW_SHOWNOACTIVATE)")
	ShowWindow.Call(hwnd, 8) // SW_SHOWNOACTIVATE = 8

	log.Printf("[DEBUG] Window creation completed successfully")
	return nil
}

// messageLoop runs the Windows message loop
func (t *Trap) messageLoop() {
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

// registerRawInput registers for raw mouse input
func (t *Trap) registerRawInput() error {
	log.Printf("Registering Raw Input device for mouse (usage page: 0x01, usage: 0x02)")

	rid := RAWINPUTDEVICE{
		UsUsagePage: 0x01, // HID_USAGE_PAGE_GENERIC
		UsUsage:     0x02, // HID_USAGE_GENERIC_MOUSE
		DwFlags:     RIDEV_INPUTSINK,
		HwndTarget:  t.hwnd,
	}

	log.Printf("Raw Input device struct: %+v", rid)

	ret, _, err := RegisterRawInputDevices.Call(
		uintptr(unsafe.Pointer(&rid)),
		1,
		uintptr(unsafe.Sizeof(rid)),
	)
	if ret == 0 {
		log.Printf("RegisterRawInputDevices call failed with return value: %d", ret)
		return fmt.Errorf("RegisterRawInputDevices failed: %v", err)
	}

	log.Printf("Raw Input device registered successfully")
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
		log.Printf("Warning: Ctrl+Alt+Esc is already registered. Trying alternative hotkeys...")

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
				log.Printf("Successfully registered alternative kill switch: %s", alt.desc)
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
	log.Printf("Received WM_INPUT message, processing raw input data")

	var size uint32 = 0

	// Get the size of the raw input data
	GetRawInputData.Call(
		lparam,
		uintptr(unsafe.Pointer(&size)),
		unsafe.Sizeof(RAWINPUTHEADER{}),
		uintptr(unsafe.Pointer(&size)),
	)

	if size == 0 {
		log.Printf("Raw input data size is 0, skipping")
		return
	}

	log.Printf("Raw input data size: %d bytes", size)

	// Allocate buffer for raw input data
	data := make([]byte, size)
	ret, _, _ := GetRawInputData.Call(
		lparam,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(size),
		unsafe.Sizeof(RAWINPUTHEADER{}),
	)

	if ret == 0xFFFFFFFF { // error
		log.Printf("GetRawInputData failed with error code: 0x%X", ret)
		return
	}

	log.Printf("Successfully retrieved %d bytes of raw input data", ret)

	// Parse the raw input data
	rawInput := (*RAWINPUT)(unsafe.Pointer(&data[0]))
	log.Printf("Raw input type: %d (expected: %d for mouse)", rawInput.Header.DwType, RIM_TYPEMOUSE)

	if rawInput.Header.DwType == RIM_TYPEMOUSE {
		log.Printf("Processing mouse input event")
		t.handleMouseInput(&rawInput.Mouse)
	} else {
		log.Printf("Ignoring non-mouse input event (type: %d)", rawInput.Header.DwType)
	}
}

// handleMouseInput processes mouse input events
func (t *Trap) handleMouseInput(mouse *RAWMOUSE) {
	log.Printf("Processing mouse input: flags=0x%X, buttons=0x%X, lastX=%d, lastY=%d",
		mouse.UsFlags, mouse.UsButtonFlags, mouse.LLastX, mouse.LLastY)

	event := InputEvent{
		Type:      "mouse_move",
		DeltaX:    int(mouse.LLastX),
		DeltaY:    int(mouse.LLastY),
		Timestamp: time.Now().UnixMilli(),
	}

	// Update virtual cursor position
	t.cursorX += event.DeltaX
	t.cursorY += event.DeltaY

	log.Printf("Updated virtual cursor position: (%d, %d)", t.cursorX, t.cursorY)

	// Check if virtual cursor is near screen boundaries and reset if needed
	const boundaryThreshold = 50
	var rect RECT
	GetClientRect.Call(uintptr(t.hwnd), uintptr(unsafe.Pointer(&rect)))

	// Get screen dimensions (simplified - in real implementation, get actual screen size)
	screenWidth := int32(1920)  // TODO: Get actual screen width
	screenHeight := int32(1080) // TODO: Get actual screen height

	needsReset := false
	if t.cursorX < boundaryThreshold || t.cursorX > int(screenWidth)-boundaryThreshold {
		needsReset = true
	}
	if t.cursorY < boundaryThreshold || t.cursorY > int(screenHeight)-boundaryThreshold {
		needsReset = true
	}

	if needsReset {
		log.Printf("Cursor near boundary, resetting position")
		// Reset virtual cursor to center of screen
		centerX := int(screenWidth / 2)
		centerY := int(screenHeight / 2)

		// Set actual cursor position to center
		SetCursorPos.Call(uintptr(centerX), uintptr(centerY))

		// Adjust virtual cursor position
		t.cursorX = centerX
		t.cursorY = centerY
	}

	// Handle mouse buttons
	if mouse.UsButtonFlags&0x0001 != 0 { // RI_MOUSE_LEFT_BUTTON_DOWN
		log.Printf("Left mouse button down")
		event.Type = "mouse_btn"
		event.Button = 1
		event.Pressed = true
	} else if mouse.UsButtonFlags&0x0002 != 0 { // RI_MOUSE_LEFT_BUTTON_UP
		log.Printf("Left mouse button up")
		event.Type = "mouse_btn"
		event.Button = 1
		event.Pressed = false
	} else if mouse.UsButtonFlags&0x0004 != 0 { // RI_MOUSE_RIGHT_BUTTON_DOWN
		log.Printf("Right mouse button down")
		event.Type = "mouse_btn"
		event.Button = 2
		event.Pressed = true
	} else if mouse.UsButtonFlags&0x0008 != 0 { // RI_MOUSE_RIGHT_BUTTON_UP
		log.Printf("Right mouse button up")
		event.Type = "mouse_btn"
		event.Button = 2
		event.Pressed = false
	} else if mouse.UsButtonFlags&0x0010 != 0 { // RI_MOUSE_MIDDLE_BUTTON_DOWN
		log.Printf("Middle mouse button down")
		event.Type = "mouse_btn"
		event.Button = 3
		event.Pressed = true
	} else if mouse.UsButtonFlags&0x0020 != 0 { // RI_MOUSE_MIDDLE_BUTTON_UP
		log.Printf("Middle mouse button up")
		event.Type = "mouse_btn"
		event.Button = 3
		event.Pressed = false
	}

	log.Printf("Sending event to channel: %+v", event)
	select {
	case t.events <- event:
		log.Printf("Event sent to channel successfully")
	default:
		log.Printf("Event channel full, dropping event")
		// Channel full, drop event
	}
}
