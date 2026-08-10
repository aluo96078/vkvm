package input

import (
	"testing"
	"time"
)

func TestEventBufferCoalescesMouseMotion(t *testing.T) {
	b := NewEventBuffer(64)
	defer b.Close()
	b.Push(InputEvent{Type: "mouse_move", DeltaX: 2, DeltaY: 3}, false)
	b.Push(InputEvent{Type: "mouse_move", DeltaX: 4, DeltaY: -1}, false)
	select {
	case got := <-b.Events():
		if got.DeltaX != 6 || got.DeltaY != 2 {
			t.Fatalf("coalesced motion = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for motion")
	}
}

func TestEventBufferCriticalPushIsNotDroppedWhenCapacityFrees(t *testing.T) {
	b := NewEventBuffer(64)
	defer b.Close()
	for i := 0; i < 64; i++ {
		if !b.Push(InputEvent{Type: "key", KeyCode: uint16(i), Pressed: i%2 == 0}, true) {
			t.Fatal("critical event rejected")
		}
	}
	read := <-b.Events()
	if read.Type != "key" {
		t.Fatalf("first event = %+v", read)
	}
	done := make(chan bool, 1)
	go func() { done <- b.Push(InputEvent{Type: "key", KeyCode: 99, Pressed: false}, true) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("critical push was dropped")
		}
	case <-time.After(time.Second):
		t.Fatal("critical push did not make progress")
	}
}
