package input

import (
	"sync"
	"testing"

	"vkvm/internal/protocol"
)

func TestInputVectorsCoverIMEModifierAndRapidTransitions(t *testing.T) {
	state := NewStateTracker()
	// Rime/IME-style chords must preserve modifier ordering: modifier down,
	// space/letter transition, then modifier up. The state tracker should not
	// infer a release from a repeat or a Caps Lock toggle.
	events := []InputEvent{
		{Type: "key", KeyCode: 0xA2, ScanCode: 0x1D, Pressed: true, Modifiers: 2}, // left Ctrl
		{Type: "key", KeyCode: 0xA0, ScanCode: 0x2A, Pressed: true, Modifiers: 3}, // left Shift
		{Type: "key", KeyCode: 0x20, ScanCode: 0x39, Pressed: true, Modifiers: 3}, // IME chord
		{Type: "key", KeyCode: 0x20, ScanCode: 0x39, Pressed: false, Modifiers: 3},
		{Type: "key", KeyCode: 0x14, ScanCode: 0x3A, Pressed: true, Modifiers: 3}, // Caps Lock
		{Type: "key", KeyCode: 0xA0, ScanCode: 0x2A, Pressed: false, Modifiers: 2},
		{Type: "key", KeyCode: 0xA2, ScanCode: 0x1D, Pressed: false},
	}
	for _, event := range events {
		state.Apply(event)
	}
	snapshot := state.Snapshot()
	if len(snapshot.Keys) != 1 || snapshot.Keys[0].KeyCode != 0x14 {
		t.Fatalf("IME/Caps state=%+v", snapshot)
	}

	// Rapid click and drag transitions must remain ordered while high-rate
	// motion is coalesced separately.
	var mu sync.Mutex
	var received []protocol.InputPayload
	queue := NewInjectionQueue(64, func(payload protocol.InputPayload) {
		mu.Lock()
		received = append(received, payload)
		mu.Unlock()
	})
	for seq, event := range []protocol.InputPayload{
		{Type: "mouse_btn", Button: 1, Pressed: true, Sequence: 1, SessionID: "rapid"},
		{Type: "mouse_move", DeltaX: 2, DeltaY: 1, Sequence: 2, SessionID: "rapid"},
		{Type: "mouse_btn", Button: 1, Pressed: false, Sequence: 3, SessionID: "rapid"},
	} {
		if !queue.Submit(event) {
			t.Fatalf("rapid event %d was rejected", seq)
		}
	}
	queue.Flush()
	queue.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 || received[0].Type != "mouse_btn" || received[1].Type != "mouse_move" || received[2].Pressed {
		t.Fatalf("rapid transition order=%+v", received)
	}
}

func TestInputVectorsCoverModifiersRolloverDragAndRelease(t *testing.T) {
	state := NewStateTracker()
	// Same virtual key with E0/scan distinction must remain two independent
	// physical keys (e.g. left/right navigation variants).
	state.Apply(InputEvent{Type: "key", KeyCode: 0x25, ScanCode: 0x4B, Extended: false, Pressed: true, Modifiers: 1})
	state.Apply(InputEvent{Type: "key", KeyCode: 0x25, ScanCode: 0x4B, Extended: true, Pressed: true, Modifiers: 1})
	// N-key rollover and explicit repeat do not erase an existing key.
	state.Apply(InputEvent{Type: "key", KeyCode: 0x1E, ScanCode: 0x1E, Pressed: true, Repeat: true, Modifiers: 3})
	state.Apply(InputEvent{Type: "key", KeyCode: 0x30, ScanCode: 0x30, Pressed: true, Modifiers: 3})
	state.Apply(InputEvent{Type: "mouse_btn", Button: 1, Pressed: true, Modifiers: 3})
	snapshot := state.Snapshot()
	if len(snapshot.Keys) != 4 || len(snapshot.Buttons) != 1 || snapshot.Modifiers != 3 {
		t.Fatalf("unexpected rollover snapshot: %+v", snapshot)
	}
	state.Apply(InputEvent{Type: "key", KeyCode: 0x25, ScanCode: 0x4B, Extended: false, Pressed: false, Modifiers: 3})
	state.Apply(InputEvent{Type: "mouse_btn", Button: 1, Pressed: false, Modifiers: 0})
	snapshot = state.Snapshot()
	if len(snapshot.Keys) != 3 || len(snapshot.Buttons) != 0 {
		t.Fatalf("release vector left stale state: %+v", snapshot)
	}
}

func TestInputProtocolVectorPreservesE1RepeatAndHighResolutionWheel(t *testing.T) {
	pkt := &protocol.UDPPacket{Type: protocol.UDPPacketKeyEvent, KeyCode: 0x45, ScanCode: 0x45, Pressed: 1, Modifiers: 0x0F, KeyFlags: 0x06}
	decoded, err := protocol.DecodeUDPPacket(protocol.EncodeUDPPacket(pkt))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ScanCode != 0x45 || decoded.KeyFlags != 0x06 || decoded.Modifiers != 0x0F {
		t.Fatalf("metadata vector lost: %+v", decoded)
	}
	wheel := InputEvent{Type: "mouse_wheel", WheelDelta: 1}
	calibrated := NewCalibrationState(Calibration{WheelPixelsPerUnit: 0.25}).Apply(wheel)
	if calibrated.WheelDelta != 0 {
		t.Fatalf("sub-unit wheel calibration should round deterministically: %+v", calibrated)
	}
}

func TestInputVectorsCoverModifiersCapsMediaRapidClickAndDisconnect(t *testing.T) {
	state := NewStateTracker()
	// Left/right modifiers and Caps Lock are represented as distinct physical
	// scan-code entries; media/Fn usages are retained even when the platform
	// injector later chooses to ignore an unmapped usage.
	for _, event := range []InputEvent{
		{Type: "key", KeyCode: 0xA0, ScanCode: 0x2A, Pressed: true, Modifiers: 1},
		{Type: "key", KeyCode: 0xA1, ScanCode: 0x36, Pressed: true, Modifiers: 1},
		{Type: "key", KeyCode: 0x14, ScanCode: 0x3A, Pressed: true, Modifiers: 1}, // Caps Lock
		{Type: "key", KeyCode: 0xB5, ScanCode: 0x20, Pressed: true, Modifiers: 1}, // media
		{Type: "mouse_btn", Button: 1, Pressed: true, Modifiers: 1},
		{Type: "mouse_move", DeltaX: 4, DeltaY: -3, Modifiers: 1}, // drag
		{Type: "mouse_btn", Button: 1, Pressed: false, Modifiers: 1},
	} {
		state.Apply(event)
	}
	snapshot := state.Snapshot()
	if len(snapshot.Keys) != 4 || len(snapshot.Buttons) != 0 {
		t.Fatalf("input vector state=%+v", snapshot)
	}
	// A disconnect/reconnect release-all must leave no stuck keys/buttons.
	state.Reset()
	if got := state.Snapshot(); len(got.Keys) != 0 || len(got.Buttons) != 0 || got.Modifiers != 0 {
		t.Fatalf("release-all left state=%+v", got)
	}
	// High polling-rate motion remains bounded by the coalescing queue.
	queue := NewInjectionQueue(4, func(protocol.InputPayload) {})
	for i := 0; i < 1000; i++ {
		queue.Submit(protocol.InputPayload{Type: "mouse_move", DeltaX: 1, Sequence: uint32(i + 1), SessionID: "poll"})
	}
	if queue.Depth() > 4 {
		t.Fatalf("high-rate queue exceeded bound: %d", queue.Depth())
	}
	queue.Close()
}
