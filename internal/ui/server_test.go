package ui

import "testing"

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
