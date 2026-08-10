package integration

import (
	"testing"
	"time"

	"vkvm/internal/input"
	"vkvm/internal/network"
	"vkvm/internal/protocol"
)

// TestInputTransportSmoke exercises the same metadata path used by the Host
// UDP sender and Agent ordered queue without requiring a physical keyboard or
// an open LAN socket.
func TestInputTransportSmoke(t *testing.T) {
	packet := &protocol.UDPPacket{Type: protocol.UDPPacketKeyEvent, Seq: 7, Timestamp: time.Now().UnixMilli(), DeviceID: "host", SessionID: "session", KeyCode: 0x41, ScanCode: 0x1e, Pressed: 1, KeyFlags: 0x05}
	encoded := protocol.EncodeAuthenticatedUDPPacket(packet, []byte("integration-secret"))
	decoded, err := protocol.DecodeAuthenticatedUDPPacket(encoded, []byte("integration-secret"))
	if err != nil || decoded.Seq != packet.Seq || decoded.ScanCode != packet.ScanCode || decoded.KeyFlags&0x01 == 0 || decoded.KeyFlags&0x04 == 0 {
		t.Fatalf("transport metadata changed: decoded=%+v err=%v", decoded, err)
	}
	metrics := input.NewMetrics(64)
	metrics.ObservePayload(protocol.InputPayload{DeviceID: decoded.DeviceID, SessionID: decoded.SessionID, Sequence: decoded.Seq, Timestamp: decoded.Timestamp})
	if metrics.Snapshot().Received != 1 {
		t.Fatal("input metrics did not observe transport event")
	}
}

func TestDiscoveryMatchSmoke(t *testing.T) {
	endpoint, err := network.MatchPairedEndpoint([]network.PairedEndpoint{{DeviceID: "host", Role: "host"}}, []network.DiscoveredHost{{DeviceID: "host", Role: "host", IP: "192.0.2.10", Port: 18080}})
	if err != nil || endpoint != "192.0.2.10:18080" {
		t.Fatalf("paired discovery endpoint=%q err=%v", endpoint, err)
	}
}
