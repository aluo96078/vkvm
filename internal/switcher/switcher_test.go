package switcher

import (
	"testing"

	"vkvm/internal/ddc"
)

func TestResolveProfileMonitorInputsIgnoresStaleAliases(t *testing.T) {
	active := []ddc.Monitor{
		{ID: `MONITOR\AUS275B\{id}\0018`, DeviceName: `\\.\DISPLAY1\Monitor0`, Name: "AUS275B"},
		{ID: `MONITOR\AUS275B\{id}\0017`, DeviceName: `\\.\DISPLAY2\Monitor0`},
	}
	configured := map[string]int{
		`MONITOR\AUS275B\{id}\0018`: int(ddc.InputSourceHDMI2),
		`\\.\DISPLAY2\Monitor0`:     int(ddc.InputSourceHDMI1),
		`MONITOR\AUS275B\{id}\0003`: int(ddc.InputSourceHDMI1),
		"AUS275B":                   int(ddc.InputSourceDP1),
	}

	resolved := resolveProfileMonitorInputs(active, configured)
	if len(resolved) != 2 {
		t.Fatalf("resolved %d monitor mappings, want 2: %#v", len(resolved), resolved)
	}
	if got := resolved[active[0].ID]; got != ddc.InputSourceHDMI2 {
		t.Fatalf("monitor 1 input=%v, want HDMI2", got)
	}
	if got := resolved[active[1].ID]; got != ddc.InputSourceHDMI1 {
		t.Fatalf("monitor 2 input=%v, want HDMI1", got)
	}
}

func TestResolveProfileMonitorInputsReturnsEmptyWhenNoActiveMatch(t *testing.T) {
	active := []ddc.Monitor{{ID: `MONITOR\AUS275B\{id}\0018`}}
	configured := map[string]int{`MONITOR\AUS275B\{id}\0003`: int(ddc.InputSourceHDMI1)}
	if resolved := resolveProfileMonitorInputs(active, configured); len(resolved) != 0 {
		t.Fatalf("resolved stale mapping: %#v", resolved)
	}
}
