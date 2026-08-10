package hid

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type CommandRunner func(context.Context, string, ...string) error

// PairBLEDevice is intentionally an OS-shell adapter, not a fake BLE stack.
// On macOS the optional blueutil utility delegates pairing/connection to the
// system Bluetooth daemon; production deployments may replace the runner with
// a CoreBluetooth/IOBluetooth helper without changing the HID session.
func PairBLEDevice(ctx context.Context, address string) error {
	return PairBLEDeviceWithRunner(ctx, address, func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Run()
	})
}

func PairBLEDeviceWithRunner(ctx context.Context, address string, runner CommandRunner) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validBluetoothAddress(address) {
		return errors.New("invalid Bluetooth device address")
	}
	if runner == nil {
		return errors.New("Bluetooth command runner is nil")
	}
	if err := runner(ctx, "blueutil", "--pair", address); err != nil {
		return fmt.Errorf("BLE pairing failed: %w", err)
	}
	if err := runner(ctx, "blueutil", "--connect", address); err != nil {
		return fmt.Errorf("BLE connection failed: %w", err)
	}
	return nil
}

func validBluetoothAddress(address string) bool {
	parts := strings.Split(address, ":")
	if len(parts) != 6 {
		return false
	}
	for _, part := range parts {
		if len(part) != 2 {
			return false
		}
		for _, char := range part {
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
				return false
			}
		}
	}
	return true
}

// HID over BLE uses a MAC address rather than an IP endpoint. Keeping the
// address parser here prevents the pairing helper from accepting arbitrary
// shell arguments.
func BluetoothAddressToHost(address string) string {
	if !validBluetoothAddress(address) {
		return ""
	}
	return strings.ToLower(address)
}
