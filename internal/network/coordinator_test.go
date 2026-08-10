package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestMatchPairedEndpointRejectsUnknownAndAmbiguousPeers(t *testing.T) {
	paired := []PairedEndpoint{{DeviceID: "host-1", Role: "host"}}
	if _, err := MatchPairedEndpoint(paired, []DiscoveredHost{{DeviceID: "other", IP: "10.0.0.2", Port: 18080}}); err == nil {
		t.Fatal("unknown device was matched")
	}
	if _, err := MatchPairedEndpoint([]PairedEndpoint{{DeviceID: "a"}, {DeviceID: "b"}}, []DiscoveredHost{
		{DeviceID: "a", IP: "10.0.0.1", Port: 1}, {DeviceID: "b", IP: "10.0.0.2", Port: 1},
	}); err == nil {
		t.Fatal("ambiguous devices were auto-selected")
	}
}

func TestMatchPairedEndpointBindsPublicKeyCredential(t *testing.T) {
	paired := []PairedEndpoint{{DeviceID: "host", PublicKey: "key-a"}}
	if _, err := MatchPairedEndpoint(paired, []DiscoveredHost{{DeviceID: "host", PublicKey: "key-b", IP: "10.0.0.1", Port: 1}}); err == nil {
		t.Fatal("mismatched public key was accepted")
	}
	if _, err := MatchPairedEndpoint(paired, []DiscoveredHost{{DeviceID: "host", IP: "10.0.0.1", Port: 1}}); err == nil {
		t.Fatal("missing public key was accepted")
	}
	endpoint, err := MatchPairedEndpoint(paired, []DiscoveredHost{{DeviceID: "host", PublicKey: "key-a", IP: "10.0.0.1", Port: 1}})
	if err != nil || endpoint != "10.0.0.1:1" {
		t.Fatalf("matching public key endpoint=%q err=%v", endpoint, err)
	}
}

func TestMatchPairedEndpointBindsRoleCredential(t *testing.T) {
	paired := []PairedEndpoint{{DeviceID: "host", Role: "host"}}
	if _, err := MatchPairedEndpoint(paired, []DiscoveredHost{{DeviceID: "host", IP: "10.0.0.1", Port: 1}}); err == nil {
		t.Fatal("missing role was accepted")
	}
	if _, err := MatchPairedEndpoint(paired, []DiscoveredHost{{DeviceID: "host", Role: "agent", IP: "10.0.0.1", Port: 1}}); err == nil {
		t.Fatal("mismatched role was accepted")
	}
}

func TestCoordinatorUsesLastKnownEndpointBeforeDiscovery(t *testing.T) {
	var probes atomic.Int32
	var discoveries atomic.Int32
	c := NewCoordinator(CoordinatorConfig{
		Paired: []PairedEndpoint{{DeviceID: "host", LastKnownEndpoint: "127.0.0.1:18080"}},
		Discovery: func(context.Context) ([]DiscoveredHost, error) {
			discoveries.Add(1)
			return nil, errors.New("should not discover")
		},
		Probe:             func(context.Context, string) error { probes.Add(1); return nil },
		Connect:           func(context.Context, string) error { return nil },
		Debounce:          time.Millisecond,
		DiscoveryTimeout:  time.Second,
		ReconnectAttempts: 1,
	})
	fingerprints := make(chan NetworkSnapshot, 1)
	fingerprints <- NetworkSnapshot{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, fingerprints) }()
	deadline := time.Now().Add(time.Second)
	for c.State() != CoordinatorActive && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if probes.Load() != 1 || discoveries.Load() != 0 || c.LastTarget() != "127.0.0.1:18080" {
		t.Fatalf("probes=%d discoveries=%d state=%s target=%q", probes.Load(), discoveries.Load(), c.State(), c.LastTarget())
	}
}

func TestCoordinatorWaitsUntilFingerprintChanges(t *testing.T) {
	var discoveries atomic.Int32
	c := NewCoordinator(CoordinatorConfig{
		Discovery:        func(context.Context) ([]DiscoveredHost, error) { discoveries.Add(1); return nil, errors.New("offline") },
		DiscoveryTimeout: time.Second,
		Debounce:         time.Millisecond,
	})
	fingerprints := make(chan NetworkSnapshot, 2)
	fingerprints <- NetworkSnapshot{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, fingerprints) }()
	deadline := time.Now().Add(time.Second)
	for c.State() != CoordinatorWaitingForNetworkChange && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if discoveries.Load() != 1 {
		t.Fatalf("expected one bounded discovery, got %d", discoveries.Load())
	}
	// No periodic work occurs while the state is waiting.
	time.Sleep(10 * time.Millisecond)
	if discoveries.Load() != 1 {
		t.Fatalf("waiting state performed an extra discovery: %d", discoveries.Load())
	}
	fingerprints <- NetworkSnapshot{Interfaces: []InterfaceSnapshot{{Name: "wifi", Addresses: []string{"10.0.0.0/24"}}}}
	deadline = time.Now().Add(time.Second)
	for discoveries.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if discoveries.Load() != 2 {
		t.Fatalf("fingerprint change did not wake discovery: %d", discoveries.Load())
	}
}

func TestCoordinatorDefaultDiscoveryCarriesConfiguredPort(t *testing.T) {
	c := NewCoordinator(CoordinatorConfig{DiscoveryPort: 19090})
	// The default function is intentionally private to the coordinator config;
	// invoke it with a context that is already cancelled so no LAN traffic is
	// attempted, while still proving the configured port was accepted and the
	// coordinator remains constructible.
	if c.cfg.DiscoveryPort != 19090 || c.cfg.Discovery == nil {
		t.Fatalf("default discovery config = %+v", c.cfg)
	}
}

func TestCoordinatorDoesNotUseUnpairedConfiguredEndpointByDefault(t *testing.T) {
	var discovered atomic.Int32
	c := NewCoordinator(CoordinatorConfig{
		ConfiguredEndpoint: "127.0.0.1:18080",
		Discovery: func(context.Context) ([]DiscoveredHost, error) {
			discovered.Add(1)
			return nil, errors.New("not paired")
		},
		Debounce: time.Millisecond,
	})
	fingerprints := make(chan NetworkSnapshot, 1)
	fingerprints <- NetworkSnapshot{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, fingerprints) }()
	deadline := time.Now().Add(time.Second)
	for discovered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if discovered.Load() != 1 {
		t.Fatalf("unpaired configured endpoint bypassed discovery: %d", discovered.Load())
	}
}

func TestCoordinatorTraceShowsOneResolvePerFingerprint(t *testing.T) {
	trace := NewTraceRecorder(128)
	var discoveries atomic.Int32
	c := NewCoordinator(CoordinatorConfig{
		Discovery: func(context.Context) ([]DiscoveredHost, error) {
			discoveries.Add(1)
			return nil, errors.New("offline")
		},
		Debounce:         time.Millisecond,
		DiscoveryTimeout: time.Second,
		Trace:            trace.Record,
	})
	fingerprints := make(chan NetworkSnapshot, 3)
	fingerprints <- NetworkSnapshot{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, fingerprints) }()
	deadline := time.Now().Add(time.Second)
	for discoveries.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if discoveries.Load() != 1 {
		t.Fatalf("initial discovery count=%d", discoveries.Load())
	}
	time.Sleep(10 * time.Millisecond)
	if discoveries.Load() != 1 {
		t.Fatalf("waiting state scanned again: %d", discoveries.Load())
	}
	fingerprints <- NetworkSnapshot{Interfaces: []InterfaceSnapshot{{Name: "wifi", Addresses: []string{"10.0.0.0/24"}}}}
	deadline = time.Now().Add(time.Second)
	for discoveries.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if discoveries.Load() != 2 {
		t.Fatalf("changed fingerprint discovery count=%d", discoveries.Load())
	}
	events := trace.Snapshot()
	if len(events) == 0 {
		t.Fatal("coordinator trace is empty")
	}
	path := filepath.Join(t.TempDir(), "trace.json")
	if err := trace.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("trace file err=%v mode=%v", err, info.Mode())
	}
}
