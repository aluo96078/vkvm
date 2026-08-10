//go:build !darwin

package input

func CurrentPermissions() PermissionStatus {
	return PermissionStatus{Accessibility: true, InputMonitoring: true}
}
