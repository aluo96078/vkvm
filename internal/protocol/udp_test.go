package protocol

import (
	"reflect"
	"testing"
)

func TestAuthenticatedUDPPacketRoundTrip(t *testing.T) {
	key := []byte("test-session-key")
	want := &UDPPacket{
		Type:      UDPPacketKeyEvent,
		Seq:       42,
		Timestamp: 1234,
		KeyCode:   0x41,
		Pressed:   1,
		Modifiers: 0x0002,
		ScanCode:  0x001e,
		KeyFlags:  0x01,
		DeviceTag: HardwareTag("hid-physical-1"),
	}
	data := EncodeAuthenticatedUDPPacket(want, key)
	got, err := DecodeAuthenticatedUDPPacket(data, key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, *want) {
		t.Fatalf("decoded packet = %+v, want %+v", got, want)
	}
}

func TestHardwareTagRoundTripAndEmptyFallback(t *testing.T) {
	if HardwareTag("") != 0 || HardwareIDFromTag(0) != "" {
		t.Fatal("empty hardware ID did not use zero tag")
	}
	want := HardwareTag("hid-physical-1")
	packet := &UDPPacket{Type: UDPPacketMouseMove, DeviceTag: want}
	got, err := DecodeUDPPacket(EncodeUDPPacket(packet))
	if err != nil || got.DeviceTag != want || HardwareIDFromTag(got.DeviceTag) == "" {
		t.Fatalf("hardware tag round-trip got=%+v err=%v", got, err)
	}
}

func TestAuthenticatedUDPPacketRejectsTamperAndWrongKey(t *testing.T) {
	data := EncodeAuthenticatedUDPPacket(&UDPPacket{
		Type: UDPPacketMouseMove,
		Seq:  1,
	}, []byte("correct"))
	data[len(data)-1] ^= 0x01
	if _, err := DecodeAuthenticatedUDPPacket(data, []byte("correct")); err == nil {
		t.Fatal("tampered packet was accepted")
	}

	data = EncodeAuthenticatedUDPPacket(&UDPPacket{Type: UDPPacketMouseMove, Seq: 2}, []byte("correct"))
	if _, err := DecodeAuthenticatedUDPPacket(data, []byte("wrong")); err == nil {
		t.Fatal("packet authenticated with wrong key")
	}
}

func TestUDPPacketIdentityRoundTrip(t *testing.T) {
	want := &UDPPacket{
		Type:            UDPPacketRegister,
		Seq:             9,
		Timestamp:       1234,
		DeviceID:        "agent-device",
		SessionID:       "session-123",
		ProtocolVersion: ProtocolVersion,
		Capabilities:    CapabilityInputMetadata | CapabilityUDP,
	}
	got, err := DecodeUDPPacket(EncodeUDPPacket(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != want.DeviceID || got.SessionID != want.SessionID ||
		got.ProtocolVersion != want.ProtocolVersion || got.Capabilities != want.Capabilities {
		t.Fatalf("identity metadata = %+v, want %+v", got, want)
	}
}

func TestAckIdentityRoundTrip(t *testing.T) {
	want := &UDPPacket{
		Type:            UDPPacketAck,
		DeviceID:        "host-device",
		SessionID:       "host-session",
		ProtocolVersion: ProtocolVersion,
		Capabilities:    CapabilityUDP | CapabilityReliableControl,
	}
	got, err := DecodeUDPPacket(EncodeUDPPacket(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != want.DeviceID || got.SessionID != want.SessionID || got.Capabilities != want.Capabilities {
		t.Fatalf("ack identity = %+v, want %+v", got, want)
	}
}

func TestLegacyHeaderOnlyRegisterRemainsDecodable(t *testing.T) {
	got, err := DecodeUDPPacket(EncodeUDPPacket(&UDPPacket{Type: UDPPacketRegister}))
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != "" || got.SessionID != "" {
		t.Fatalf("unexpected identity metadata: %+v", got)
	}
}

func TestInputAckRoundTrip(t *testing.T) {
	want := &UDPPacket{Type: UDPPacketInputAck, Seq: 77, AckSeq: 42, AckType: UDPPacketKeyEvent}
	got, err := DecodeUDPPacket(EncodeUDPPacket(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.AckSeq != want.AckSeq || got.AckType != want.AckType {
		t.Fatalf("ack = %+v, want %+v", got, want)
	}
}

func TestStateSnapshotRoundTrip(t *testing.T) {
	want := &UDPPacket{
		Type: UDPPacketStateSnapshot, Seq: 8, StateModifiers: 3,
		StateKeys:    []KeyState{{KeyCode: 0x10, ScanCode: 0x2a}, {KeyCode: 0x11, ScanCode: 0x1d, Extended: true, E1: true}},
		StateButtons: []uint8{1, 5},
	}
	got, err := DecodeUDPPacket(EncodeUDPPacket(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.StateModifiers != want.StateModifiers || len(got.StateKeys) != 2 || len(got.StateButtons) != 2 || !got.StateKeys[1].Extended || !got.StateKeys[1].E1 {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
}

func TestUDPPacketRejectsMalformedFrames(t *testing.T) {
	cases := [][]byte{
		nil,
		make([]byte, UDPHeaderSize), // unknown type 0
		append([]byte{UDPPacketMouseMove}, make([]byte, UDPHeaderSize-1+7)...),
	}
	for _, data := range cases {
		if _, err := DecodeUDPPacket(data); err == nil {
			t.Fatalf("malformed UDP frame accepted: %x", data)
		}
	}
	if _, err := DecodeAuthenticatedUDPPacket([]byte("short"), []byte("key")); err == nil {
		t.Fatal("short authenticated frame accepted")
	}
	if _, err := DecodeAuthenticatedUDPPacketWithKeys(nil, nil); err == nil {
		t.Fatal("empty key ring accepted")
	}
}

func TestUDPPacketRoundTripsAllInputKinds(t *testing.T) {
	packets := []*UDPPacket{
		{Type: UDPPacketMouseMove, DeltaX: -3, DeltaY: 5},
		{Type: UDPPacketMouseButton, Button: 5, Pressed: 1},
		{Type: UDPPacketMouseScroll, WheelDelta: -120, Axis: 1},
		{Type: UDPPacketReleaseAll},
		{Type: UDPPacketAck},
	}
	for _, want := range packets {
		got, err := DecodeUDPPacket(EncodeUDPPacket(want))
		if err != nil || got.Type != want.Type {
			t.Fatalf("packet type=%d got=%+v err=%v", want.Type, got, err)
		}
	}
	long := &UDPPacket{Type: UDPPacketRegister, DeviceID: string(make([]byte, 200)), SessionID: string(make([]byte, 200))}
	got, err := DecodeUDPPacket(EncodeUDPPacket(long))
	if err != nil || len(got.DeviceID) != maxUDPIdentityField || len(got.SessionID) != maxUDPIdentityField {
		t.Fatalf("identity truncation device=%d session=%d err=%v", len(got.DeviceID), len(got.SessionID), err)
	}
}
