package hid

import (
	"context"
	"errors"
	"testing"
)

func TestPairBLEDeviceValidatesAddressAndUsesSystemAdapter(t *testing.T) {
	var calls [][]string
	err := PairBLEDeviceWithRunner(context.Background(), "AA:BB:CC:DD:EE:FF", func(_ context.Context, command string, args ...string) error {
		calls = append(calls, append([]string{command}, args...))
		return nil
	})
	if err != nil || len(calls) != 2 || calls[0][1] != "--pair" || calls[1][1] != "--connect" {
		t.Fatalf("pair calls=%v err=%v", calls, err)
	}
	if err := PairBLEDeviceWithRunner(context.Background(), "not-an-address", func(context.Context, string, ...string) error { return nil }); err == nil {
		t.Fatal("invalid BLE address accepted")
	}
	if err := PairBLEDeviceWithRunner(context.Background(), "AA:BB:CC:DD:EE:FF", func(context.Context, string, ...string) error { return errors.New("offline") }); err == nil {
		t.Fatal("pairing command error was swallowed")
	}
}
