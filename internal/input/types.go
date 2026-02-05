// Package input provides cross-platform input capture and injection functionality.
package input

// InputEvent represents a keyboard or mouse input event
type InputEvent struct {
	Type       string `json:"type"` // "mouse_move", "mouse_btn", "mouse_wheel", "key"
	DeltaX     int    `json:"dx,omitempty"`
	DeltaY     int    `json:"dy,omitempty"`
	Button     int    `json:"btn,omitempty"` // 1=left, 2=right, 3=middle, 4=xbutton1, 5=xbutton2
	Pressed    bool   `json:"pressed,omitempty"`
	KeyCode    uint16 `json:"keycode,omitempty"`
	Modifiers  uint16 `json:"modifiers,omitempty"`
	WheelDelta int    `json:"wheel_delta,omitempty"` // Positive=up/right, Negative=down/left
	Timestamp  int64  `json:"ts"`                    // Unix ms timestamp
}

// InputCapture defines the interface for capturing input events
type InputCapture interface {
	Start() error
	Stop() error
	Events() <-chan InputEvent
}

// InputInjector defines the interface for injecting input events
type InputInjector interface {
	InjectMouseMove(dx, dy int) error
	InjectMouseButton(button int, pressed bool) error
	InjectMouseWheel(deltaY, deltaX int) error
	InjectKey(keyCode uint16, pressed bool, modifiers uint16) error
}
