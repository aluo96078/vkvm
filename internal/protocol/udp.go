package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
)

// UDP Packet types
const (
	UDPPacketMouseMove     uint8 = 0x01
	UDPPacketMouseButton   uint8 = 0x02
	UDPPacketMouseScroll   uint8 = 0x03
	UDPPacketKeyEvent      uint8 = 0x04
	UDPPacketReleaseAll    uint8 = 0x05
	UDPPacketStateSnapshot uint8 = 0x06
	UDPPacketRegister      uint8 = 0x10
	UDPPacketHeartbeat     uint8 = 0x11
	UDPPacketAck           uint8 = 0x12 // Host -> Agent: confirms UDP path is open
	UDPPacketInputAck      uint8 = 0x13 // Agent -> Host: confirms critical input delivery
)

// Header: [type(1)] [seq(4)] [timestamp(8)] = 13 bytes. Authenticated
// transports append UDPAuthTagSize bytes after the packet body.
const UDPHeaderSize = 13

// UDPAuthTagSize is a truncated HMAC-SHA256 appended to every authenticated
// control/input packet. The shared API token is used as the session key until
// a dedicated pairing key is introduced.
const UDPAuthTagSize = 16

const (
	maxUDPIdentityField = 128
	udpIdentityVersion  = 1
)

// UDPPacket represents a binary-encoded input event for low-latency UDP transport.
//
// Wire format per type:
//
//	MouseMove   (0x01): header + dx(int32) + dy(int32) + deviceTag(uint32) = 25 bytes
//	MouseButton (0x02): header + button(uint8) + pressed(uint8) + deviceTag(uint32) = 19 bytes
//	MouseScroll (0x03): header + delta(int32) + axis(uint8) + deviceTag(uint32) = 22 bytes
//	KeyEvent    (0x04): header + keyCode(uint16) + scanCode(uint16) + pressed(uint8) + mods(uint16) + flags(uint8) + deviceTag(uint32) = 25 bytes
//	Register    (0x10): header + bounded identity payload
//	Heartbeat   (0x11): header + bounded identity payload
type UDPPacket struct {
	Type           uint8
	Seq            uint32
	Timestamp      int64
	DeltaX         int32  // mouse move
	DeltaY         int32  // mouse move
	Button         uint8  // mouse button (1-5)
	Pressed        uint8  // mouse button / key (1=pressed, 0=released)
	WheelDelta     int32  // scroll delta
	Axis           uint8  // scroll axis: 0=vertical, 1=horizontal
	KeyCode        uint16 // key code
	Modifiers      uint16 // key modifiers bitmask
	ScanCode       uint16 // physical scan code, when available
	KeyFlags       uint8  // bit 0=extended/E0, bit 1=E1, bit 2=repeat
	AckSeq         uint32 // input sequence acknowledged by UDPPacketInputAck
	AckType        uint8  // original input packet type
	StateModifiers uint16
	StateKeys      []KeyState
	StateButtons   []uint8
	// Identity metadata is carried on register/heartbeat packets. Input
	// packets are bound to the most recent authenticated registration for the
	// source endpoint, so they remain compact while still being session scoped.
	DeviceID        string
	SessionID       string
	ProtocolVersion uint16
	Capabilities    uint32
	DeviceTag       uint32 // stable per-session hash of the physical input device
}

// HardwareTag keeps physical-device metadata compact on UDP. The full
// HardwareID remains on the authenticated WebSocket path; the Agent receives
// a non-secret, collision-resistant session tag for calibration lookup.
func HardwareTag(hardwareID string) uint32 {
	if hardwareID == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(hardwareID))
	return h.Sum32()
}

func HardwareIDFromTag(tag uint32) string {
	if tag == 0 {
		return ""
	}
	return fmt.Sprintf("hid-tag-%08x", tag)
}

// EncodeUDPPacket serializes a UDPPacket to wire format.
func EncodeUDPPacket(pkt *UDPPacket) []byte {
	size := UDPHeaderSize
	switch pkt.Type {
	case UDPPacketMouseMove:
		size += 12 // dx(4) + dy(4) + physical device tag(4)
	case UDPPacketMouseButton:
		size += 6 // button(1) + pressed(1) + physical device tag(4)
	case UDPPacketMouseScroll:
		size += 9 // delta(4) + axis(1) + physical device tag(4)
	case UDPPacketKeyEvent:
		size += 12 // keyCode(2) + scanCode(2) + pressed(1) + modifiers(2) + flags(1) + tag(4)
	case UDPPacketInputAck:
		size += 5 // ack sequence(4) + original packet type(1)
	case UDPPacketStateSnapshot:
		keyCount := len(pkt.StateKeys)
		if keyCount > 255 {
			keyCount = 255
		}
		buttonCount := len(pkt.StateButtons)
		if buttonCount > 255 {
			buttonCount = 255
		}
		size += 4 + keyCount*5 + buttonCount
	case UDPPacketRegister, UDPPacketHeartbeat, UDPPacketAck:
		size += udpIdentityPayloadSize(pkt)
	}

	buf := make([]byte, size)
	buf[0] = pkt.Type
	binary.BigEndian.PutUint32(buf[1:5], pkt.Seq)
	binary.BigEndian.PutUint64(buf[5:13], uint64(pkt.Timestamp))

	payload := buf[UDPHeaderSize:]
	switch pkt.Type {
	case UDPPacketMouseMove:
		binary.BigEndian.PutUint32(payload[0:4], uint32(pkt.DeltaX))
		binary.BigEndian.PutUint32(payload[4:8], uint32(pkt.DeltaY))
		binary.BigEndian.PutUint32(payload[8:12], pkt.DeviceTag)
	case UDPPacketMouseButton:
		payload[0] = pkt.Button
		payload[1] = pkt.Pressed
		binary.BigEndian.PutUint32(payload[2:6], pkt.DeviceTag)
	case UDPPacketMouseScroll:
		binary.BigEndian.PutUint32(payload[0:4], uint32(pkt.WheelDelta))
		payload[4] = pkt.Axis
		binary.BigEndian.PutUint32(payload[5:9], pkt.DeviceTag)
	case UDPPacketKeyEvent:
		binary.BigEndian.PutUint16(payload[0:2], pkt.KeyCode)
		binary.BigEndian.PutUint16(payload[2:4], pkt.ScanCode)
		payload[4] = pkt.Pressed
		binary.BigEndian.PutUint16(payload[5:7], pkt.Modifiers)
		payload[7] = pkt.KeyFlags
		binary.BigEndian.PutUint32(payload[8:12], pkt.DeviceTag)
	case UDPPacketInputAck:
		binary.BigEndian.PutUint32(payload[0:4], pkt.AckSeq)
		payload[4] = pkt.AckType
	case UDPPacketStateSnapshot:
		keyCount := len(pkt.StateKeys)
		if keyCount > 255 {
			keyCount = 255
		}
		buttonCount := len(pkt.StateButtons)
		if buttonCount > 255 {
			buttonCount = 255
		}
		binary.BigEndian.PutUint16(payload[0:2], pkt.StateModifiers)
		payload[2] = byte(keyCount)
		payload[3] = byte(buttonCount)
		pos := 4
		for _, key := range pkt.StateKeys[:keyCount] {
			binary.BigEndian.PutUint16(payload[pos:pos+2], key.KeyCode)
			binary.BigEndian.PutUint16(payload[pos+2:pos+4], key.ScanCode)
			if key.Extended {
				payload[pos+4] |= 0x01
			}
			if key.E1 {
				payload[pos+4] |= 0x02
			}
			pos += 5
		}
		copy(payload[pos:], pkt.StateButtons[:buttonCount])
	case UDPPacketRegister, UDPPacketHeartbeat, UDPPacketAck:
		encodeUDPIdentity(payload, pkt)
	}

	return buf
}

// DecodeUDPPacket deserializes wire bytes into a UDPPacket.
func DecodeUDPPacket(data []byte) (*UDPPacket, error) {
	if len(data) < UDPHeaderSize {
		return nil, errors.New("udp: packet too short")
	}

	pkt := &UDPPacket{
		Type:      data[0],
		Seq:       binary.BigEndian.Uint32(data[1:5]),
		Timestamp: int64(binary.BigEndian.Uint64(data[5:13])),
	}

	payload := data[UDPHeaderSize:]
	switch pkt.Type {
	case UDPPacketMouseMove:
		if len(payload) < 8 {
			return nil, errors.New("udp: mouse move payload too short")
		}
		pkt.DeltaX = int32(binary.BigEndian.Uint32(payload[0:4]))
		pkt.DeltaY = int32(binary.BigEndian.Uint32(payload[4:8]))
		if len(payload) >= 12 {
			pkt.DeviceTag = binary.BigEndian.Uint32(payload[8:12])
		}
	case UDPPacketInputAck:
		if len(payload) < 5 {
			return nil, errors.New("udp: input ack payload too short")
		}
		pkt.AckSeq = binary.BigEndian.Uint32(payload[0:4])
		pkt.AckType = payload[4]
	case UDPPacketStateSnapshot:
		if len(payload) < 4 {
			return nil, errors.New("udp: state snapshot payload too short")
		}
		keyCount, buttonCount := int(payload[2]), int(payload[3])
		need := 4 + keyCount*5 + buttonCount
		if len(payload) < need {
			return nil, errors.New("udp: truncated state snapshot")
		}
		pkt.StateModifiers = binary.BigEndian.Uint16(payload[0:2])
		pkt.StateKeys = make([]KeyState, 0, keyCount)
		pos := 4
		for i := 0; i < keyCount; i++ {
			flags := payload[pos+4]
			pkt.StateKeys = append(pkt.StateKeys, KeyState{
				KeyCode:  binary.BigEndian.Uint16(payload[pos : pos+2]),
				ScanCode: binary.BigEndian.Uint16(payload[pos+2 : pos+4]),
				Extended: flags&0x01 != 0,
				E1:       flags&0x02 != 0,
			})
			pos += 5
		}
		pkt.StateButtons = append([]uint8(nil), payload[pos:pos+buttonCount]...)
	case UDPPacketMouseButton:
		if len(payload) < 2 {
			return nil, errors.New("udp: mouse button payload too short")
		}
		pkt.Button = payload[0]
		pkt.Pressed = payload[1]
		if len(payload) >= 6 {
			pkt.DeviceTag = binary.BigEndian.Uint32(payload[2:6])
		}
	case UDPPacketMouseScroll:
		if len(payload) < 5 {
			return nil, errors.New("udp: mouse scroll payload too short")
		}
		pkt.WheelDelta = int32(binary.BigEndian.Uint32(payload[0:4]))
		pkt.Axis = payload[4]
		if len(payload) >= 9 {
			pkt.DeviceTag = binary.BigEndian.Uint32(payload[5:9])
		}
	case UDPPacketKeyEvent:
		if len(payload) < 5 {
			return nil, errors.New("udp: key event payload too short")
		}
		pkt.KeyCode = binary.BigEndian.Uint16(payload[0:2])
		if len(payload) >= 8 {
			pkt.ScanCode = binary.BigEndian.Uint16(payload[2:4])
			pkt.Pressed = payload[4]
			pkt.Modifiers = binary.BigEndian.Uint16(payload[5:7])
			pkt.KeyFlags = payload[7]
			if len(payload) >= 12 {
				pkt.DeviceTag = binary.BigEndian.Uint32(payload[8:12])
			}
		} else {
			// Decode the original 5-byte key payload for compatibility.
			pkt.Pressed = payload[2]
			pkt.Modifiers = binary.BigEndian.Uint16(payload[3:5])
		}
	case UDPPacketRegister, UDPPacketHeartbeat, UDPPacketAck:
		if err := decodeUDPIdentity(payload, pkt); err != nil {
			return nil, err
		}
	case UDPPacketReleaseAll:
		// no payload
	default:
		return nil, errors.New("udp: unknown packet type")
	}

	return pkt, nil
}

func udpIdentityPayloadSize(pkt *UDPPacket) int {
	deviceLen := len(pkt.DeviceID)
	if deviceLen > maxUDPIdentityField {
		deviceLen = maxUDPIdentityField
	}
	sessionLen := len(pkt.SessionID)
	if sessionLen > maxUDPIdentityField {
		sessionLen = maxUDPIdentityField
	}
	// version(1), device length(1), session length(1), device, session,
	// protocol version(2), capability mask(4).
	return 1 + 1 + 1 + deviceLen + sessionLen + 2 + 4
}

func encodeUDPIdentity(dst []byte, pkt *UDPPacket) {
	if len(dst) < udpIdentityPayloadSize(pkt) {
		return
	}
	device := []byte(pkt.DeviceID)
	if len(device) > maxUDPIdentityField {
		device = device[:maxUDPIdentityField]
	}
	session := []byte(pkt.SessionID)
	if len(session) > maxUDPIdentityField {
		session = session[:maxUDPIdentityField]
	}
	dst[0] = udpIdentityVersion
	dst[1] = byte(len(device))
	dst[2] = byte(len(session))
	pos := 3
	copy(dst[pos:], device)
	pos += len(device)
	copy(dst[pos:], session)
	pos += len(session)
	binary.BigEndian.PutUint16(dst[pos:pos+2], pkt.ProtocolVersion)
	pos += 2
	binary.BigEndian.PutUint32(dst[pos:pos+4], pkt.Capabilities)
}

func decodeUDPIdentity(src []byte, pkt *UDPPacket) error {
	if len(src) == 0 {
		// Header-only registration remains decodable for legacy peers. The
		// caller can reject it when pairing/identity is required.
		return nil
	}
	if len(src) < 7 {
		return errors.New("udp: identity payload too short")
	}
	if src[0] != udpIdentityVersion {
		return errors.New("udp: unsupported identity payload version")
	}
	deviceLen, sessionLen := int(src[1]), int(src[2])
	if deviceLen > maxUDPIdentityField || sessionLen > maxUDPIdentityField {
		return errors.New("udp: identity field too long")
	}
	need := 3 + deviceLen + sessionLen + 2 + 4
	if len(src) < need {
		return errors.New("udp: truncated identity payload")
	}
	pos := 3
	pkt.DeviceID = string(src[pos : pos+deviceLen])
	pos += deviceLen
	pkt.SessionID = string(src[pos : pos+sessionLen])
	pos += sessionLen
	pkt.ProtocolVersion = binary.BigEndian.Uint16(src[pos : pos+2])
	pos += 2
	pkt.Capabilities = binary.BigEndian.Uint32(src[pos : pos+4])
	return nil
}

// EncodeAuthenticatedUDPPacket appends a truncated HMAC-SHA256 tag to the
// normal packet encoding. A missing key is rejected by callers; keeping this
// helper deterministic makes it suitable for the UDP probe and data paths.
func EncodeAuthenticatedUDPPacket(pkt *UDPPacket, key []byte) []byte {
	body := EncodeUDPPacket(pkt)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	tag := mac.Sum(nil)[:UDPAuthTagSize]
	return append(body, tag...)
}

// DecodeAuthenticatedUDPPacket verifies the tag before decoding any payload.
func DecodeAuthenticatedUDPPacket(data, key []byte) (*UDPPacket, error) {
	if len(key) == 0 {
		return nil, errors.New("udp: authentication key is empty")
	}
	if len(data) < UDPHeaderSize+UDPAuthTagSize {
		return nil, errors.New("udp: authenticated packet too short")
	}
	body := data[:len(data)-UDPAuthTagSize]
	receivedTag := data[len(data)-UDPAuthTagSize:]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	expectedTag := mac.Sum(nil)[:UDPAuthTagSize]
	if !hmac.Equal(receivedTag, expectedTag) {
		return nil, errors.New("udp: authentication failed")
	}
	return DecodeUDPPacket(body)
}

// DecodeAuthenticatedUDPPacketWithKeys accepts the current key and a bounded
// set of recently retired keys during coordinated credential rotation.
func DecodeAuthenticatedUDPPacketWithKeys(data []byte, keys [][]byte) (*UDPPacket, error) {
	if len(keys) == 0 {
		return nil, errors.New("udp: authentication keys are empty")
	}
	var lastErr error
	for _, key := range keys {
		if len(key) == 0 {
			continue
		}
		pkt, err := DecodeAuthenticatedUDPPacket(data, key)
		if err == nil {
			return pkt, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("udp: authentication keys are empty")
	}
	return nil, lastErr
}
