package network

import (
	"net"
	"testing"
	"time"

	"vkvm/internal/protocol"
)

func TestUDPSenderRoutesOnlyHealthyUDPAgents(t *testing.T) {
	s := NewUDPSender(0, "secret")
	s.agents["udp"] = &udpAgent{
		addr:     &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 18080},
		deviceID: "udp-agent", sessionID: "s1", lastSeen: time.Now(),
		capabilities: protocol.CapabilityUDP,
	}
	s.agents["ws"] = &udpAgent{
		addr:     &net.UDPAddr{IP: net.ParseIP("192.0.2.11"), Port: 18080},
		deviceID: "ws-agent", sessionID: "s2", lastSeen: time.Now(),
		capabilities: protocol.CapabilityWebSocket,
	}
	s.agents["stale"] = &udpAgent{
		addr:     &net.UDPAddr{IP: net.ParseIP("192.0.2.12"), Port: 18080},
		deviceID: "stale-agent", sessionID: "s3", lastSeen: time.Now().Add(-time.Minute),
		capabilities: protocol.CapabilityUDP,
	}
	if !s.HasDevice("udp-agent") || s.HasDevice("ws-agent") || s.HasDevice("stale-agent") {
		t.Fatalf("unexpected routing state: udp=%v ws=%v stale=%v", s.HasDevice("udp-agent"), s.HasDevice("ws-agent"), s.HasDevice("stale-agent"))
	}
	if len(s.TransportSnapshot()) != 3 {
		t.Fatal("transport snapshot did not preserve per-endpoint state")
	}
}

func TestUDPSenderDerivesPerAgentSessionKey(t *testing.T) {
	s := NewUDPSender(0, "secret")
	a := &udpAgent{deviceID: "agent", sessionID: "session"}
	key := s.agentKey(a)
	want := protocol.DeriveSessionKey("secret", "agent", "session")
	if string(key) != string(want) {
		t.Fatal("agent key is not bound to device/session identity")
	}
	if string(key) == string(s.authKey()) {
		t.Fatal("agent key unexpectedly equals shared token")
	}
}
