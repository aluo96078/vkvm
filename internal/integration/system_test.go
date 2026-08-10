package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"vkvm/internal/config"
	"vkvm/internal/hid"
	"vkvm/internal/network"
	"vkvm/internal/service"
)

func TestAuthenticatedPairDiscoveryAndBLEFallbackFlow(t *testing.T) {
	m, err := config.NewManagerAt(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	m.Set(config.DefaultConfig())
	if err := m.PairDevice(config.PairedDevice{DeviceID: "host", Role: "host", PublicKey: m.Get().General.IdentityPublicKey}); err != nil {
		t.Fatal(err)
	}
	endpoint, err := network.MatchPairedEndpoint([]network.PairedEndpoint{{DeviceID: "host", Role: "host", PublicKey: m.Get().General.IdentityPublicKey}}, []network.DiscoveredHost{{DeviceID: "host", Role: "host", PublicKey: m.Get().General.IdentityPublicKey, IP: "192.0.2.10", Port: 18080}})
	if err != nil || endpoint != "192.0.2.10:18080" {
		t.Fatalf("pair discovery endpoint=%q err=%v", endpoint, err)
	}
	bleTransport := &hid.MemoryBLETransport{}
	bleSession := hid.NewBLESession(bleTransport, hid.DefaultBLEHIDProfile("integration"), time.Second)
	if err := bleSession.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	bleSink := hid.NewBLEInputSink(bleSession)
	lanSink := &integrationSink{}
	router := hid.NewBLERouter(bleSink, lanSink, true, func() bool { return bleTransport.State() != hid.BLEIdle })
	if err := router.Send(hid.InputEvent{Type: "key", KeyCode: 0x41, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if len(bleTransport.Reports()) == 0 || lanSink.sent != 0 {
		t.Fatalf("BLE-first flow reports=%d lan=%d", len(bleTransport.Reports()), lanSink.sent)
	}
	if err := router.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceSupervisorAndCoordinatorShutdownFlow(t *testing.T) {
	status := service.NewStatusStore("test")
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := service.NewSupervisor(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, nil, service.SupervisorConfig{HealthInterval: time.Millisecond, Status: status})
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	fingerprints := make(chan network.NetworkSnapshot, 1)
	fingerprints <- network.NetworkSnapshot{}
	coordinator := network.NewCoordinator(network.CoordinatorConfig{Discovery: func(context.Context) ([]network.DiscoveredHost, error) { return nil, errors.New("offline") }, Debounce: time.Millisecond, DiscoveryTimeout: time.Second})
	coordDone := make(chan error, 1)
	coordCtx, coordCancel := context.WithCancel(context.Background())
	go func() { coordDone <- coordinator.Run(coordCtx, fingerprints) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	coordCancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("supervisor shutdown err=%v", err)
	}
	if err := <-coordDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("coordinator shutdown err=%v", err)
	}
}

type integrationSink struct{ sent int }

func (s *integrationSink) Send(hid.InputEvent) error { s.sent++; return nil }
func (s *integrationSink) ReleaseAll() error         { return nil }
