package hid

import (
	"context"
	"testing"
	"time"
)

func TestUSBHIDBackendEmitsBootReportsAndWatchdogRelease(t *testing.T) {
	writer := &MemoryUSBWriter{}
	backend := NewUSBHIDBackend(writer, DefaultBLEHIDProfile("usb-test"), 20*time.Millisecond)
	if err := backend.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !backend.Healthy() {
		t.Fatal("USB backend is not healthy after start")
	}
	if err := backend.Send(InputEvent{Type: "key", KeyCode: 0x41, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Send(InputEvent{Type: "mouse_btn", Button: 1, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Send(InputEvent{Type: "mouse_move", DeltaX: 300, DeltaY: -260}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := backend.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	keyboards, mice := writer.Reports()
	if len(keyboards) < 2 || len(mice) < 3 {
		t.Fatalf("unexpected USB report counts keyboards=%d mice=%d", len(keyboards), len(mice))
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.Healthy() {
		t.Fatal("closed USB backend still reports healthy")
	}
}

func TestUSBHIDTransportRejectsInvalidWriterAndReports(t *testing.T) {
	transport := NewUSBHIDTransport(nil)
	if err := transport.Start(context.Background(), BootKeyboardMouseDescriptor); err == nil {
		t.Fatal("nil USB writer accepted")
	}
	writer := &MemoryUSBWriter{}
	transport = NewUSBHIDTransport(writer)
	if err := transport.Start(context.Background(), BootKeyboardMouseDescriptor); err != nil {
		t.Fatal(err)
	}
	if err := transport.Send(BLEReport{Kind: "unknown"}); err == nil {
		t.Fatal("unknown USB report kind accepted")
	}
}
