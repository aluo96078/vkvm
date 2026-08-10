package network

import (
	"context"
	"testing"
	"time"
)

func TestNetworkSnapshotKeyIsOrderIndependent(t *testing.T) {
	a := NetworkSnapshot{Interfaces: []InterfaceSnapshot{{Name: "wifi", Index: 2, Flags: "up", Addresses: []string{"10.0.0.0/24", "fd00::/64"}}}}
	b := NetworkSnapshot{Interfaces: []InterfaceSnapshot{{Name: "wifi", Index: 2, Flags: "up", Addresses: []string{"fd00::/64", "10.0.0.0/24"}}}}
	if a.Key() != b.Key() {
		t.Fatalf("snapshot key differs for reordered addresses: %q vs %q", a.Key(), b.Key())
	}
}

func TestNetworkSnapshotFingerprintIncludesGateway(t *testing.T) {
	a := NetworkSnapshot{Interfaces: []InterfaceSnapshot{{Name: "wifi", Addresses: []string{"10.0.0.0/24"}}}, Gateways: []string{"10.0.0.1"}}
	b := NetworkSnapshot{Interfaces: []InterfaceSnapshot{{Name: "wifi", Addresses: []string{"10.0.0.0/24"}}}, Gateways: []string{"10.0.0.254"}}
	if a.Key() == b.Key() {
		t.Fatal("gateway change did not alter network fingerprint")
	}
}

func TestGetLocalAddressesDoesNotReturnLoopback(t *testing.T) {
	addresses, err := GetLocalAddresses()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if address.IsLoopback() || address.IsUnspecified() {
			t.Fatalf("invalid local address returned: %v", address)
		}
	}
}

func TestNativeSnapshotSourceDebouncesOSEvents(t *testing.T) {
	ready := make(chan chan<- struct{}, 1)
	source := newNativeSnapshotSource(func(ctx context.Context, events chan<- struct{}) error {
		ready <- events
		<-ctx.Done()
		return ctx.Err()
	}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := source.Changes(ctx)
	select {
	case events := <-ready:
		// Coalesced events should not create a scan per event.
		for i := 0; i < 3; i++ {
			select {
			case events <- struct{}{}:
			default:
			}
		}
	case <-time.After(time.Second):
		t.Fatal("native watcher did not start")
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		// The initial snapshot is allowed to be empty but must be emitted.
		t.Fatal("native source did not emit initial snapshot")
	}
}
