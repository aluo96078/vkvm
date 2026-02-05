package input

import (
	"testing"
	"time"
)

// TestInputEventCreation tests that input events are created correctly
func TestInputEventCreation(t *testing.T) {
	event := InputEvent{
		Type:      "mouse_move",
		DeltaX:    10,
		DeltaY:    -5,
		Timestamp: time.Now().UnixMilli(),
	}

	if event.Type != "mouse_move" {
		t.Errorf("Expected event type 'mouse_move', got '%s'", event.Type)
	}
	if event.DeltaX != 10 {
		t.Errorf("Expected DeltaX 10, got %d", event.DeltaX)
	}
	if event.DeltaY != -5 {
		t.Errorf("Expected DeltaY -5, got %d", event.DeltaY)
	}
}

// TestInputEventButton tests mouse button events
func TestInputEventButton(t *testing.T) {
	event := InputEvent{
		Type:    "mouse_btn",
		Button:  1,
		Pressed: true,
	}

	if event.Type != "mouse_btn" {
		t.Errorf("Expected event type 'mouse_btn', got '%s'", event.Type)
	}
	if event.Button != 1 {
		t.Errorf("Expected button 1, got %d", event.Button)
	}
	if !event.Pressed {
		t.Error("Expected button to be pressed")
	}
}

// TestInputEventKey tests keyboard events
func TestInputEventKey(t *testing.T) {
	event := InputEvent{
		Type:      "key",
		KeyCode:   0x41, // 'A' key
		Pressed:   true,
		Modifiers: 0x0002, // Shift
	}

	if event.Type != "key" {
		t.Errorf("Expected event type 'key', got '%s'", event.Type)
	}
	if event.KeyCode != 0x41 {
		t.Errorf("Expected key code 0x41, got 0x%X", event.KeyCode)
	}
	if !event.Pressed {
		t.Error("Expected key to be pressed")
	}
	if event.Modifiers != 0x0002 {
		t.Errorf("Expected modifiers 0x0002, got 0x%X", event.Modifiers)
	}
}
