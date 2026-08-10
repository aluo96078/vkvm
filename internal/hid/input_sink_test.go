package hid

import (
	"context"
	"testing"
)

func TestBLEInputSinkConvertsKeyDragAndRelease(t *testing.T) {
	transport := &MemoryBLETransport{}
	session := NewBLESession(transport, DefaultBLEHIDProfile("test"), 0)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink := NewBLEInputSink(session)
	if err := sink.Send(InputEvent{Type: "key", KeyCode: 0x41, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Send(InputEvent{Type: "mouse_btn", Button: 1, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Send(InputEvent{Type: "mouse_move", DeltaX: 300, DeltaY: -260}); err != nil {
		t.Fatal(err)
	}
	if err := sink.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	reports := transport.Reports()
	if len(reports) < 4 || reports[len(reports)-1].Kind != "release_all" {
		t.Fatalf("reports=%+v", reports)
	}
}

func TestBLEInputSinkPreservesPhysicalModifiersAndConvertsMask(t *testing.T) {
	transport := &MemoryBLETransport{}
	session := NewBLESession(transport, DefaultBLEHIDProfile("modifier"), 0)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink := NewBLEInputSink(session)
	// Generic VK_SHIFT with the right-shift scan code must become HID E5,
	// represented in the modifier byte rather than the six-key array.
	if err := sink.Send(InputEvent{Type: "key", KeyCode: 0x10, ScanCode: 0x36, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Send(InputEvent{Type: "key", KeyCode: 0x41, Pressed: true, Modifiers: 0x01}); err != nil {
		t.Fatal(err)
	}
	reports := transport.Reports()
	if len(reports) < 2 || reports[0].Keyboard[0]&0x02 == 0 || reports[1].Keyboard[2] != 0x04 {
		t.Fatalf("modifier reports=%+v", reports)
	}
	if _, ok := VKToHIDUsageWithScan(0x11, 0x1D, false); !ok {
		t.Fatal("left control mapping missing")
	}
	if usage, ok := VKToHIDUsageWithScan(0x11, 0x1D, true); !ok || usage != 0xE4 {
		t.Fatalf("right control mapping usage=0x%X ok=%v", usage, ok)
	}
}
