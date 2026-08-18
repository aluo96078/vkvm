package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"vkvm/internal/config"
)

func TestValidateRemoteAddress(t *testing.T) {
	valid := []string{"192.168.1.10:18080", "10.0.0.2:443", "[fd00::10]:18080", "100.64.0.2:18080"}
	for _, address := range valid {
		if _, err := validateRemoteAddress(address); err != nil {
			t.Errorf("validateRemoteAddress(%q) returned error: %v", address, err)
		}
	}
	invalid := []string{"127.0.0.1:18080", "0.0.0.0:1", "224.0.0.1:1", "8.8.8.8:53", "localhost:80", "192.168.1.1:0", "192.168.1.1:65536"}
	for _, address := range invalid {
		if _, err := validateRemoteAddress(address); err == nil {
			t.Errorf("validateRemoteAddress(%q) unexpectedly succeeded", address)
		}
	}
}

func TestSwitchHandlerRoutesToCore(t *testing.T) {
	m, err := config.NewManagerAt(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	called := ""
	s := NewServer(m, nil)
	s.SetSwitchHandler(func(profile string) error {
		called = profile
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/switch?profile=PC2", nil)
	resp := httptest.NewRecorder()
	s.handleSwitch(resp, req)
	if resp.Code != http.StatusOK || called != "PC2" {
		t.Fatalf("status=%d called=%q body=%s", resp.Code, called, resp.Body.String())
	}
}

func TestConfigSaveHandlerForwardsSnapshot(t *testing.T) {
	m, err := config.NewManagerAt(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.Get()
	cfg.General.CurrentProfile = "PC2"
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var forwarded *config.Config
	s := NewServer(m, nil)
	s.SetConfigSaveHandler(func(saved *config.Config) error {
		forwarded = saved
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(data))
	resp := httptest.NewRecorder()
	s.handleConfig(resp, req)
	if resp.Code != http.StatusOK || forwarded == nil || forwarded.General.CurrentProfile != "PC2" {
		t.Fatalf("status=%d forwarded=%v body=%s", resp.Code, forwarded != nil, resp.Body.String())
	}
	if got := m.Get().General.CurrentProfile; got != "PC2" {
		t.Fatalf("local config profile=%q", got)
	}
}
