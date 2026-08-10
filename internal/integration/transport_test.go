package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"vkvm/internal/input"
	"vkvm/internal/network"
	"vkvm/internal/protocol"
)

// TestLoopbackUDPTransport exercises registration, authenticated input,
// per-agent routing and release-all over real localhost UDP sockets. It is a
// portable substitute for the two-machine leg of the integration matrix.
func TestLoopbackUDPTransport(t *testing.T) {
	sender := network.NewUDPSender(0, "integration-secret")
	sender.SetIdentity("host", "host-session", protocol.CapabilityUDP|protocol.CapabilityReliableControl)
	if err := sender.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("sandbox disallows UDP sockets: %v", err)
		}
		t.Fatal(err)
	}
	defer sender.Stop()
	port := sender.LocalPort()
	if port == 0 {
		t.Fatal("sender did not expose an ephemeral port")
	}

	receiver := network.NewUDPReceiver(fmt.Sprintf("127.0.0.1:%d", port), "integration-secret", "agent")
	receiver.SetExpectedPeerDeviceID("host")
	inputCh := make(chan protocol.InputPayload, 1)
	releaseCh := make(chan struct{}, 1)
	receiver.OnInputEvent = func(payload protocol.InputPayload) { inputCh <- payload }
	receiver.OnReleaseAll = func() { releaseCh <- struct{}{} }
	if !receiver.Probe() {
		t.Fatal("loopback UDP probe failed")
	}
	if err := receiver.Start(); err != nil {
		t.Fatal(err)
	}
	defer receiver.Stop()

	deadline := time.Now().Add(time.Second)
	for !sender.HasDevice("agent") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !sender.HasDevice("agent") {
		t.Fatal("agent registration was not observed")
	}
	sender.SendInputEvent(networkInputKey())
	select {
	case payload := <-inputCh:
		if payload.Type != "key" || payload.KeyCode != 0x41 || !payload.Pressed {
			t.Fatalf("unexpected loopback payload: %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("loopback input was not delivered")
	}
	sender.SendReleaseAll()
	select {
	case <-releaseCh:
	case <-time.After(time.Second):
		t.Fatal("release-all was not delivered")
	}
}

func TestLoopbackUDPTransportRoutesToMultipleAgents(t *testing.T) {
	sender := network.NewUDPSender(0, "multi-agent-secret")
	sender.SetIdentity("host", "host-session", protocol.CapabilityUDP|protocol.CapabilityReliableControl)
	if err := sender.Start(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("sandbox disallows UDP sockets: %v", err)
		}
		t.Fatal(err)
	}
	defer sender.Stop()

	type endpoint struct {
		receiver *network.UDPReceiver
		input    chan protocol.InputPayload
		release  chan struct{}
	}
	endpoints := make([]endpoint, 0, 2)
	for _, deviceID := range []string{"agent-a", "agent-b"} {
		r := network.NewUDPReceiver(fmt.Sprintf("127.0.0.1:%d", sender.LocalPort()), "multi-agent-secret", deviceID)
		r.SetExpectedPeerDeviceID("host")
		e := endpoint{receiver: r, input: make(chan protocol.InputPayload, 1), release: make(chan struct{}, 1)}
		r.OnInputEvent = func(ch chan protocol.InputPayload) func(protocol.InputPayload) {
			return func(payload protocol.InputPayload) { ch <- payload }
		}(e.input)
		r.OnReleaseAll = func(ch chan struct{}) func() {
			return func() { ch <- struct{}{} }
		}(e.release)
		if !r.Probe() {
			t.Fatalf("UDP probe failed for %s", deviceID)
		}
		if err := r.Start(); err != nil {
			t.Fatalf("UDP start failed for %s: %v", deviceID, err)
		}
		endpoints = append(endpoints, e)
	}
	defer func() {
		for _, e := range endpoints {
			e.receiver.Stop()
		}
	}()
	deadline := time.Now().Add(time.Second)
	for (!sender.HasDevice("agent-a") || !sender.HasDevice("agent-b")) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !sender.HasDevice("agent-a") || !sender.HasDevice("agent-b") {
		t.Fatalf("registered agents=%+v", sender.TransportSnapshot())
	}
	sender.SendInputEvent(networkInputKey())
	for i, e := range endpoints {
		select {
		case payload := <-e.input:
			if payload.Type != "key" || payload.KeyCode != 0x41 || !payload.Pressed {
				t.Fatalf("agent %d payload=%+v", i, payload)
			}
		case <-time.After(time.Second):
			t.Fatalf("agent %d did not receive input", i)
		}
	}
	sender.SendReleaseAll()
	for i, e := range endpoints {
		select {
		case <-e.release:
		case <-time.After(time.Second):
			t.Fatalf("agent %d did not receive release-all", i)
		}
	}
}

func networkInputKey() input.InputEvent {
	return input.InputEvent{Type: "key", KeyCode: 0x41, ScanCode: 0x1E, Pressed: true, Modifiers: 1, Timestamp: time.Now().UnixMilli()}
}
