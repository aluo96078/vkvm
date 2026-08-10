package hid

import (
	"context"
	"testing"
	"time"
)

func TestBLEProfileSessionAndWatchdog(t *testing.T) {
	transport := &MemoryBLETransport{}
	session := NewBLESession(transport, DefaultBLEHIDProfile("test"), 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.SendKeyboard(KeyboardReport{0, 0, 4}); err != nil {
		t.Fatal(err)
	}
	go session.Watchdog(ctx)
	time.Sleep(30 * time.Millisecond)
	if len(transport.Reports()) < 2 {
		t.Fatal("watchdog did not emit release report")
	}
	if transport.Reports()[0].Sequence == 0 {
		t.Fatal("BLE sequence was not assigned")
	}
	_ = session.Close()
}

type eventSink struct{ sent, released int }

func (s *eventSink) Send(InputEvent) error { s.sent++; return nil }
func (s *eventSink) ReleaseAll() error     { s.released++; return nil }

func TestBLERouterPrefersBLEThenFallsBackToLAN(t *testing.T) {
	ble, lan := &eventSink{}, &eventSink{}
	healthy := true
	router := NewBLERouter(ble, lan, true, func() bool { return healthy })
	if err := router.Send(InputEvent{Type: "key"}); err != nil || ble.sent != 1 || lan.sent != 0 {
		t.Fatalf("BLE route failed ble=%d lan=%d err=%v", ble.sent, lan.sent, err)
	}
	healthy = false
	if err := router.Send(InputEvent{Type: "key"}); err != nil || lan.sent != 1 {
		t.Fatalf("LAN fallback failed ble=%d lan=%d err=%v", ble.sent, lan.sent, err)
	}
	if err := router.ReleaseAll(); err != nil || ble.released != 1 || lan.released != 1 {
		t.Fatalf("release all failed ble=%d lan=%d err=%v", ble.released, lan.released, err)
	}
}

type failingSink struct{}

func (failingSink) Send(InputEvent) error { return ErrBLEUnavailable }
func (failingSink) ReleaseAll() error     { return nil }

func TestBLERouterFallsBackWhenBLESendFails(t *testing.T) {
	lan := &eventSink{}
	router := NewBLERouter(failingSink{}, lan, true, func() bool { return true })
	if err := router.Send(InputEvent{Type: "mouse_move", DeltaX: 1}); err != nil {
		t.Fatal(err)
	}
	if lan.sent != 1 {
		t.Fatalf("LAN fallback count=%d", lan.sent)
	}
}

func TestBLEBackendLifecycleAndProfileValidation(t *testing.T) {
	backend, transport := NewMemoryBLEBackend(DefaultBLEHIDProfile("backend"), time.Second)
	if err := backend.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !backend.Healthy() {
		t.Fatal("memory BLE backend was not healthy after start")
	}
	if err := backend.Send(InputEvent{Type: "key", KeyCode: 0x41, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if err := backend.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if len(transport.Reports()) < 2 {
		t.Fatalf("backend reports=%+v", transport.Reports())
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.Healthy() {
		t.Fatal("closed BLE backend still reports healthy")
	}
	if err := ValidateHIDProfile(HIDProfile{}); err == nil {
		t.Fatal("invalid empty HID profile accepted")
	}
}

func TestBLECompatibilityVectors(t *testing.T) {
	for _, vector := range []struct {
		vk   uint16
		want byte
	}{
		{0x14, 0x39}, // Caps Lock
		{0x7C, 0x68}, // F13
		{0x83, 0x6F}, // F20
		{0xA0, 0xE1}, // left Shift
		{0xA1, 0xE5}, // right Shift
		{0xAD, 0},    // media key is intentionally unsupported by the boot map
	} {
		got, ok := VKToHIDUsage(vector.vk)
		if vector.want == 0 {
			if ok {
				t.Fatalf("unexpected boot usage for media key 0x%X: 0x%X", vector.vk, got)
			}
			continue
		}
		if !ok || got != vector.want {
			t.Fatalf("VK 0x%X usage=0x%X ok=%v want=0x%X", vector.vk, got, ok, vector.want)
		}
	}
	if report := EncodeMouse(1, 400, -400, 200); report[1] != 127 || report[2] != 128 || report[3] != 127 {
		t.Fatalf("mouse report clamp=%v", report)
	}
}
