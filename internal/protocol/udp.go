package protocol

import (
	"encoding/binary"
	"errors"
)

// UDP Packet types
const (
	UDPPacketMouseMove   uint8 = 0x01
	UDPPacketMouseButton uint8 = 0x02
	UDPPacketMouseScroll uint8 = 0x03
	UDPPacketKeyEvent    uint8 = 0x04
	UDPPacketRegister    uint8 = 0x10
	UDPPacketHeartbeat   uint8 = 0x11
	UDPPacketAck         uint8 = 0x12 // Host -> Agent: confirms UDP path is open
)

// Header: [type(1)] [seq(4)] [timestamp(8)] = 13 bytes
const UDPHeaderSize = 13

// UDPPacket represents a binary-encoded input event for low-latency UDP transport.
//
// Wire format per type:
//
//	MouseMove   (0x01): header + dx(int32) + dy(int32)                          = 21 bytes
//	MouseButton (0x02): header + button(uint8) + pressed(uint8)                 = 15 bytes
//	MouseScroll (0x03): header + delta(int32) + axis(uint8)                     = 18 bytes
//	KeyEvent    (0x04): header + keyCode(uint16) + pressed(uint8) + mods(uint16)= 18 bytes
//	Register    (0x10): header only                                             = 13 bytes
//	Heartbeat   (0x11): header only                                             = 13 bytes
type UDPPacket struct {
	Type       uint8
	Seq        uint32
	Timestamp  int64
	DeltaX     int32  // mouse move
	DeltaY     int32  // mouse move
	Button     uint8  // mouse button (1-5)
	Pressed    uint8  // mouse button / key (1=pressed, 0=released)
	WheelDelta int32  // scroll delta
	Axis       uint8  // scroll axis: 0=vertical, 1=horizontal
	KeyCode    uint16 // key code
	Modifiers  uint16 // key modifiers bitmask
}

// EncodeUDPPacket serializes a UDPPacket to wire format.
func EncodeUDPPacket(pkt *UDPPacket) []byte {
	size := UDPHeaderSize
	switch pkt.Type {
	case UDPPacketMouseMove:
		size += 8 // dx(4) + dy(4)
	case UDPPacketMouseButton:
		size += 2 // button(1) + pressed(1)
	case UDPPacketMouseScroll:
		size += 5 // delta(4) + axis(1)
	case UDPPacketKeyEvent:
		size += 5 // keyCode(2) + pressed(1) + modifiers(2)
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
	case UDPPacketMouseButton:
		payload[0] = pkt.Button
		payload[1] = pkt.Pressed
	case UDPPacketMouseScroll:
		binary.BigEndian.PutUint32(payload[0:4], uint32(pkt.WheelDelta))
		payload[4] = pkt.Axis
	case UDPPacketKeyEvent:
		binary.BigEndian.PutUint16(payload[0:2], pkt.KeyCode)
		payload[2] = pkt.Pressed
		binary.BigEndian.PutUint16(payload[3:5], pkt.Modifiers)
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
	case UDPPacketMouseButton:
		if len(payload) < 2 {
			return nil, errors.New("udp: mouse button payload too short")
		}
		pkt.Button = payload[0]
		pkt.Pressed = payload[1]
	case UDPPacketMouseScroll:
		if len(payload) < 5 {
			return nil, errors.New("udp: mouse scroll payload too short")
		}
		pkt.WheelDelta = int32(binary.BigEndian.Uint32(payload[0:4]))
		pkt.Axis = payload[4]
	case UDPPacketKeyEvent:
		if len(payload) < 5 {
			return nil, errors.New("udp: key event payload too short")
		}
		pkt.KeyCode = binary.BigEndian.Uint16(payload[0:2])
		pkt.Pressed = payload[2]
		pkt.Modifiers = binary.BigEndian.Uint16(payload[3:5])
	case UDPPacketRegister, UDPPacketHeartbeat, UDPPacketAck:
		// no payload
	default:
		return nil, errors.New("udp: unknown packet type")
	}

	return pkt, nil
}
