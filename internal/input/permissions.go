package input

// PermissionStatus reports the minimum user-session permissions required by
// the current platform's capture/injection backend.
type PermissionStatus struct {
	Accessibility   bool `json:"accessibility"`
	InputMonitoring bool `json:"input_monitoring"`
}

func (p PermissionStatus) CanCapture() bool { return p.Accessibility && p.InputMonitoring }
func (p PermissionStatus) CanInject() bool  { return p.Accessibility }
