package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfigFailsClosed(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.General.APIEnabled {
		t.Fatal("API must be disabled by default")
	}
	if cfg.General.USBForwardingEnabled {
		t.Fatal("USB forwarding must be disabled by default")
	}
}

func TestGetReturnsDeepSnapshot(t *testing.T) {
	m := &Manager{config: DefaultConfig()}
	snapshot := m.Get()
	snapshot.General.CurrentProfile = "mutated"
	snapshot.Profiles[0].Name = "mutated"
	snapshot.Profiles[0].MonitorInputs["display"] = 99

	current := m.Get()
	if current.General.CurrentProfile == "mutated" || current.Profiles[0].Name == "mutated" {
		t.Fatal("Get returned mutable manager state")
	}
	if _, ok := current.Profiles[0].MonitorInputs["display"]; ok {
		t.Fatal("Get returned mutable nested map")
	}
}

func TestLoadCallbacksRunAfterUnlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"general":{"current_profile":"Loaded"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	m := &Manager{configPath: path, config: DefaultConfig()}
	done := make(chan struct{})
	m.RegisterChangeCallback(func() {
		if got := m.Get().General.CurrentProfile; got != "Loaded" {
			t.Errorf("callback saw profile %q, want Loaded", got)
		}
		close(done)
	})
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Load callback did not complete; possible lock held during callback")
	}
}

func TestLoadPersistsGeneratedIdentityForLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"general":{"device_id":"legacy"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{configPath: path, config: DefaultConfig()}
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	first := m.Get().General.IdentityPublicKey
	if first == "" {
		t.Fatal("legacy config did not receive identity key")
	}
	m2 := &Manager{configPath: path, config: DefaultConfig()}
	if err := m2.Load(); err != nil {
		t.Fatal(err)
	}
	if m2.Get().General.IdentityPublicKey != first {
		t.Fatal("generated identity was not persisted")
	}
}

func TestUpdatePairedEndpointOnlyUpdatesExistingPeer(t *testing.T) {
	m := &Manager{config: DefaultConfig()}
	if err := m.PairDevice(PairedDevice{DeviceID: "host", Role: "host"}); err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC)
	if !m.UpdatePairedEndpoint("host", "192.0.2.10:18080", seen) {
		t.Fatal("existing paired endpoint was not updated")
	}
	peer := m.Get().General.PairedDevices[0]
	if peer.LastKnownEndpoint != "192.0.2.10:18080" || !peer.LastSeenAt.Equal(seen) {
		t.Fatalf("endpoint metadata=%+v", peer)
	}
	if m.UpdatePairedEndpoint("unknown", "192.0.2.11:18080", seen) {
		t.Fatal("unknown endpoint created an implicit pairing")
	}
}

func TestSaveUsesPrivateFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	m := &Manager{configPath: path, config: DefaultConfig()}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Fatalf("config mode = %o, want 600", mode)
	}
}

func TestPairingAllowlistFailsClosedAndPersists(t *testing.T) {
	m := &Manager{config: DefaultConfig()}
	if m.IsPaired("agent-1") {
		t.Fatal("unpaired device was accepted")
	}
	if err := m.PairDevice(PairedDevice{DeviceID: "agent-1", Name: "Agent", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	if !m.IsPaired("agent-1") || m.IsPaired("agent-2") {
		t.Fatal("pairing allowlist did not enforce exact identity")
	}
	if !m.UnpairDevice("agent-1") || m.IsPaired("agent-1") {
		t.Fatal("unpair did not revoke identity")
	}
}

func TestPairingCanBeExplicitlyDisabledForLegacyMigration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.General.PairingRequired = false
	m := &Manager{config: cfg}
	if !m.IsPaired("legacy-device") {
		t.Fatal("explicitly disabled pairing should preserve token-authenticated legacy mode")
	}
	if m.IsPaired("") {
		t.Fatal("empty identity must never be accepted")
	}
}

func TestClassifyChangeSeparatesImmediateAndRestartFields(t *testing.T) {
	before := DefaultConfig()
	after := cloneConfig(before)
	after.General.APIPort++
	after.General.USBForwardingEnabled = true
	after.General.APIToken = "rotated"
	after.General.SettingsHotkey = "Ctrl+Alt+X"
	change := ClassifyChange(before, after)
	if !change.RequiresRestart() || !contains(change.RestartRequired, "general.api_port") {
		t.Fatalf("restart fields = %#v", change.RestartRequired)
	}
	for _, field := range []string{"general.usb_forwarding_enabled", "general.api_token", "general.settings_hotkey"} {
		if !contains(change.Immediate, field) {
			t.Fatalf("immediate fields %#v missing %q", change.Immediate, field)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
