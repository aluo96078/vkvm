package input

import "testing"

func TestPermissionStatusGatesCaptureAndInjection(t *testing.T) {
	status := PermissionStatus{Accessibility: true, InputMonitoring: false}
	if status.CanCapture() {
		t.Fatal("capture must require both permissions")
	}
	if !status.CanInject() {
		t.Fatal("injection only requires accessibility")
	}
}
