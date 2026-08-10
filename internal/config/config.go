// Package config provides configuration management for the KVM switcher.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"time"

	"vkvm/internal/security"
)

// Config represents the application configuration
type Config struct {
	// Profiles contains all computer switching profiles
	Profiles []Profile `json:"profiles"`

	// Monitors contains detected monitor information
	Monitors []MonitorInfo `json:"monitors"`

	// General contains general application settings
	General GeneralConfig `json:"general"`
}

// RemoteHost represents a remote computer to notify during profile switching
type RemoteHost struct {
	// Address is the remote host address (e.g., "192.168.1.100:8080")
	Address string `json:"address"`

	// ProfileName is the profile name to trigger on the remote host
	ProfileName string `json:"profile_name"`
}

// Profile represents a computer/device profile
type Profile struct {
	// Name is the profile name (e.g., "PC1", "Mac", "Laptop")
	Name string `json:"name"`

	// Hotkey is the keyboard shortcut to switch to this profile
	Hotkey string `json:"hotkey"`

	// MonitorInputs maps monitor ID to input source for this profile
	MonitorInputs map[string]int `json:"monitor_inputs"`

	// RemoteHosts contains remote computers to notify (optional)
	RemoteHosts []RemoteHost `json:"remote_hosts,omitempty"`

	// SwitchMode determines how switching is performed
	// Values: "local" (DDC only), "remote" (notify only), "both" (default)
	SwitchMode string `json:"switch_mode,omitempty"`
}

// MonitorInfo contains basic information about a detected monitor
type MonitorInfo struct {
	// ID is the monitor's unique identifier
	ID string `json:"id"`

	// Name is the monitor's display name
	Name string `json:"name"`

	// Serial is the monitor's serial number or UUID
	Serial string `json:"serial,omitempty"`
}

// PairedDevice is an explicitly trusted peer identity. DeviceID is public
// metadata, while the API/transport token remains the authentication secret.
type PairedDevice struct {
	DeviceID          string    `json:"device_id"`
	PublicKey         string    `json:"public_key,omitempty"`
	Name              string    `json:"name,omitempty"`
	Role              string    `json:"role,omitempty"`
	Enabled           bool      `json:"enabled"`
	LastKnownEndpoint string    `json:"last_known_endpoint,omitempty"`
	LastSeenAt        time.Time `json:"last_seen_at,omitempty"`
	AddedAt           time.Time `json:"added_at"`
}

// DeviceCalibrationConfig overrides pointer/scroll conversion for one
// authenticated physical input source. Hardware IDs are session-scoped by
// the platform capture backend; the empty map keeps the global calibration as
// the safe fallback.
type DeviceCalibrationConfig struct {
	MouseSensitivity   float64 `json:"mouse_sensitivity,omitempty"`
	WheelPixelsPerUnit float64 `json:"wheel_pixels_per_unit,omitempty"`
	NaturalScrolling   *bool   `json:"natural_scrolling,omitempty"`
	MouseAcceleration  string  `json:"mouse_acceleration,omitempty"`
}

// GeneralConfig contains general application settings
type GeneralConfig struct {
	// DeviceID is a persistent, non-secret identity used for pairing and
	// diagnostics. It must never be treated as an authentication credential.
	DeviceID string `json:"device_id,omitempty"`

	// Identity keys bind a persistent DeviceID to a pairing credential. The
	// private key is stored only in the owner-readable config file.
	IdentityPublicKey  string `json:"identity_public_key,omitempty"`
	IdentityPrivateKey string `json:"identity_private_key,omitempty"`

	// StartOnBoot determines if app starts on system boot
	StartOnBoot bool `json:"start_on_boot"`

	// StartMinimized starts the app minimized to tray
	StartMinimized bool `json:"start_minimized"`

	// ShowNotifications shows desktop notifications on switch
	ShowNotifications bool `json:"show_notifications"`

	// CurrentProfile is the currently active profile
	CurrentProfile string `json:"current_profile"`

	// APIEnabled enables the HTTP API server for remote switching
	APIEnabled bool `json:"api_enabled"`

	// APIPort is the port for the API server (default: 8080)
	APIPort int `json:"api_port"`

	// APIToken is required when APIEnabled is true and is shared by the
	// authenticated HTTP, WebSocket and UDP control planes.
	APIToken string `json:"api_token,omitempty"`

	// Role determines if this machine is a "host" or "agent"
	Role string `json:"role,omitempty"`

	// CoordinatorAddr is the Address:Port of the host machine (mandatory for agents)
	CoordinatorAddr string `json:"coordinator_addr,omitempty"`

	// ThisComputerIP is the IP address of this computer (auto-detected or manual)
	ThisComputerIP string `json:"this_computer_ip,omitempty"`

	// SettingsHotkey is the global hotkey to open the settings UI (e.g. "Ctrl+Alt+S")
	SettingsHotkey string `json:"settings_hotkey,omitempty"`

	// SleepHotkey is the global hotkey to put displays to sleep (e.g. "Ctrl+Alt+P")
	SleepHotkey string `json:"sleep_hotkey,omitempty"`

	// AgentProfile is the profile name for this agent (used to auto-detect when to inject input)
	AgentProfile string `json:"agent_profile,omitempty"`

	// InputCaptureEnabled enables complete input capture mode on Host (prevents local system from receiving input)
	InputCaptureEnabled bool `json:"input_capture_enabled"`

	// USBForwardingEnabled enables USB input forwarding (keyboard/mouse capture and injection)
	USBForwardingEnabled bool `json:"usb_forwarding_enabled"`

	// PairingRequired rejects authenticated peers whose persistent DeviceID is
	// not in PairedDevices. It defaults to true; pairing is managed through the
	// authenticated local/API administration surface.
	PairingRequired bool `json:"pairing_required"`

	// PairedDevices is the explicit allowlist used by HTTP-adjacent transports.
	PairedDevices []PairedDevice `json:"paired_devices,omitempty"`

	// EscapeHotkey is the emergency hotkey to disable input capture (e.g. "Ctrl+Alt+Shift+Esc")
	EscapeHotkey string `json:"escape_hotkey,omitempty"`

	// Input calibration is applied on the Agent after transport, preserving
	// host raw deltas while allowing per-device pointer/scroll tuning.
	MouseSensitivity   float64                            `json:"mouse_sensitivity"`
	WheelPixelsPerUnit float64                            `json:"wheel_pixels_per_unit"`
	NaturalScrolling   bool                               `json:"natural_scrolling"`
	MouseAcceleration  string                             `json:"mouse_acceleration,omitempty"` // none, linear, enhanced
	DeviceCalibrations map[string]DeviceCalibrationConfig `json:"device_calibrations,omitempty"`
}

// ChangeSummary makes configuration application semantics explicit. Settings
// that affect sockets, process role, or the authenticated transport are read
// at service start and require a restart; the remaining settings are applied
// through Manager callbacks immediately.
type ChangeSummary struct {
	Immediate       []string `json:"applied_immediately"`
	RestartRequired []string `json:"restart_required"`
}

func (s ChangeSummary) RequiresRestart() bool { return len(s.RestartRequired) > 0 }

// ClassifyChange compares two snapshots and returns stable field names for
// the API/UI. It deliberately avoids treating redacted token snapshots as a
// token rotation when both values are empty/equal.
func ClassifyChange(before, after *Config) ChangeSummary {
	if before == nil || after == nil {
		return ChangeSummary{}
	}
	var out ChangeSummary
	if before.General.APIEnabled != after.General.APIEnabled {
		out.RestartRequired = append(out.RestartRequired, "general.api_enabled")
	}
	if before.General.APIPort != after.General.APIPort {
		out.RestartRequired = append(out.RestartRequired, "general.api_port")
	}
	if before.General.APIToken != after.General.APIToken {
		out.Immediate = append(out.Immediate, "general.api_token")
	}
	if before.General.Role != after.General.Role {
		out.RestartRequired = append(out.RestartRequired, "general.role")
	}
	if before.General.CoordinatorAddr != after.General.CoordinatorAddr {
		out.RestartRequired = append(out.RestartRequired, "general.coordinator_addr")
	}
	if before.General.DeviceID != after.General.DeviceID {
		out.RestartRequired = append(out.RestartRequired, "general.device_id")
	}
	if before.General.IdentityPublicKey != after.General.IdentityPublicKey || before.General.IdentityPrivateKey != after.General.IdentityPrivateKey {
		out.RestartRequired = append(out.RestartRequired, "general.identity_key")
	}
	if before.General.USBForwardingEnabled != after.General.USBForwardingEnabled {
		out.Immediate = append(out.Immediate, "general.usb_forwarding_enabled")
	}
	if before.General.InputCaptureEnabled != after.General.InputCaptureEnabled {
		out.Immediate = append(out.Immediate, "general.input_capture_enabled")
	}
	if before.General.AgentProfile != after.General.AgentProfile {
		out.Immediate = append(out.Immediate, "general.agent_profile")
	}
	if before.General.StartOnBoot != after.General.StartOnBoot {
		out.Immediate = append(out.Immediate, "general.start_on_boot")
	}
	if before.General.StartMinimized != after.General.StartMinimized {
		out.Immediate = append(out.Immediate, "general.start_minimized")
	}
	if before.General.SettingsHotkey != after.General.SettingsHotkey {
		out.Immediate = append(out.Immediate, "general.settings_hotkey")
	}
	if before.General.SleepHotkey != after.General.SleepHotkey {
		out.Immediate = append(out.Immediate, "general.sleep_hotkey")
	}
	if before.General.EscapeHotkey != after.General.EscapeHotkey {
		out.Immediate = append(out.Immediate, "general.escape_hotkey")
	}
	if before.General.ShowNotifications != after.General.ShowNotifications {
		out.Immediate = append(out.Immediate, "general.show_notifications")
	}
	if before.General.ThisComputerIP != after.General.ThisComputerIP {
		out.Immediate = append(out.Immediate, "general.this_computer_ip")
	}
	if before.General.CurrentProfile != after.General.CurrentProfile {
		out.Immediate = append(out.Immediate, "general.current_profile")
	}
	if !reflect.DeepEqual(before.Profiles, after.Profiles) {
		out.Immediate = append(out.Immediate, "profiles")
	}
	if !reflect.DeepEqual(before.Monitors, after.Monitors) {
		out.Immediate = append(out.Immediate, "monitors")
	}
	if !reflect.DeepEqual(before.General.PairedDevices, after.General.PairedDevices) || before.General.PairingRequired != after.General.PairingRequired {
		out.Immediate = append(out.Immediate, "general.pairing")
	}
	return out
}

// DefaultConfig returns a new Config with sensible defaults
func DefaultConfig() *Config {
	identity, _ := security.NewIdentity()
	return &Config{
		Profiles: []Profile{
			{
				Name:          "PC1",
				Hotkey:        "Ctrl+Alt+1",
				MonitorInputs: make(map[string]int),
			},
			{
				Name:          "PC2",
				Hotkey:        "Ctrl+Alt+2",
				MonitorInputs: make(map[string]int),
			},
		},
		Monitors: []MonitorInfo{},
		General: GeneralConfig{
			DeviceID:             newDeviceID(),
			IdentityPublicKey:    identity.PublicString(),
			IdentityPrivateKey:   identity.PrivateString(),
			StartOnBoot:          false,
			StartMinimized:       true,
			ShowNotifications:    true,
			CurrentProfile:       "PC1",
			APIEnabled:           false, // Fail closed until the user enables and pairs the API
			APIPort:              18080,
			Role:                 "host",
			SettingsHotkey:       "Ctrl+Alt+S",
			InputCaptureEnabled:  false,
			USBForwardingEnabled: false, // Require explicit opt-in for remote input injection
			PairingRequired:      true,
			PairedDevices:        []PairedDevice{},
			EscapeHotkey:         "Ctrl+Alt+Shift+Esc",
			MouseSensitivity:     1,
			WheelPixelsPerUnit:   3,
			MouseAcceleration:    "none",
		},
	}
}

// Manager handles loading and saving configuration
type Manager struct {
	mu         sync.RWMutex
	configPath string
	config     *Config
	onChanged  []func()
}

// NewManager creates a new configuration manager
func NewManager() (*Manager, error) {
	configPath, err := getConfigPath()
	if err != nil {
		return nil, err
	}

	return &Manager{
		configPath: configPath,
		config:     DefaultConfig(),
	}, nil
}

// NewManagerAt creates a manager backed by an explicit path. It is useful for
// isolated integration tests and for embedders that supply their own config
// location; normal desktop callers should use NewManager.
func NewManagerAt(configPath string) (*Manager, error) {
	if configPath == "" {
		return nil, fmt.Errorf("config path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return nil, err
	}
	return &Manager{configPath: configPath, config: DefaultConfig()}, nil
}

// getConfigPath returns the path to the configuration file
func getConfigPath() (string, error) {
	var configDir string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, "Library", "Application Support", "vkvm")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		configDir = filepath.Join(appData, "vkvm")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, ".config", "vkvm")
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", err
	}

	return filepath.Join(configDir, "config.json"), nil
}

// Load reads the configuration from disk
func (m *Manager) Load() error {
	data, err := os.ReadFile(m.configPath)
	if os.IsNotExist(err) {
		// No config file, use defaults
		// Persist the generated identity so pairing remains stable across
		// restarts. Failure is returned because a non-persistent identity would
		// make an existing pairing ambiguous.
		return m.Save()
	}
	if err != nil {
		return err
	}

	// Start from defaults so fields added in a newer version retain safe
	// defaults when loading an older configuration file.
	loaded := DefaultConfig()
	if err := json.Unmarshal(data, loaded); err != nil {
		return err
	}
	identityGenerated := false
	if loaded.General.DeviceID == "" {
		loaded.General.DeviceID = newDeviceID()
		identityGenerated = true
	}
	// Detect older files that predate the identity key fields. The defaults
	// already contain a generated key, so inspect the persisted representation
	// separately to decide whether it must be written back for stability.
	var persisted struct {
		General struct {
			IdentityPublicKey  string `json:"identity_public_key"`
			IdentityPrivateKey string `json:"identity_private_key"`
		} `json:"general"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		return err
	}
	if persisted.General.IdentityPublicKey == "" || persisted.General.IdentityPrivateKey == "" {
		identityGenerated = true
	}

	m.mu.Lock()
	m.config = cloneConfig(loaded)
	m.mu.Unlock()
	m.notifyChanged()
	if identityGenerated {
		if err := m.Save(); err != nil {
			return fmt.Errorf("persist generated device identity: %w", err)
		}
	}
	return nil
}

func newDeviceID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("device-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// Save writes the configuration to disk
func (m *Manager) Save() error {
	m.mu.RLock()
	snapshot := cloneConfig(m.config)
	configPath := m.configPath
	m.mu.RUnlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	log.Printf("Config: Saving configuration to %s (%d bytes)", configPath, len(data))
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return err
	}
	return os.Chmod(configPath, 0600)
}

// Get returns an immutable snapshot of the current configuration. Callers
// must use Set or one of the update methods to change configuration state.
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneConfig(m.config)
}

// Set updates the configuration
func (m *Manager) Set(config *Config) {
	if config == nil {
		return
	}
	m.mu.Lock()
	m.config = cloneConfig(config)
	m.mu.Unlock()
	m.notifyChanged()
}

func (m *Manager) notifyChanged() {
	m.mu.RLock()
	callbacks := append([]func(){}, m.onChanged...)
	m.mu.RUnlock()
	for _, cb := range callbacks {
		if cb != nil {
			cb()
		}
	}
}

// SetCurrentProfile updates only the active profile and notifies observers.
func (m *Manager) SetCurrentProfile(name string) {
	m.mu.Lock()
	if m.config.General.CurrentProfile == name {
		m.mu.Unlock()
		return
	}
	m.config.General.CurrentProfile = name
	m.mu.Unlock()
	m.notifyChanged()
}

// RegisterChangeCallback registers a function to be called when config changes
func (m *Manager) RegisterChangeCallback(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		return
	}
	m.onChanged = append(m.onChanged, fn)
}

// GetProfile returns a profile by name
func (m *Manager) GetProfile(name string) *Profile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.config.Profiles {
		if m.config.Profiles[i].Name == name {
			profile := cloneProfile(m.config.Profiles[i])
			return &profile
		}
	}
	return nil
}

// SetProfile updates or adds a profile
func (m *Manager) SetProfile(profile Profile) {
	m.mu.Lock()
	changed := false
	for i := range m.config.Profiles {
		if m.config.Profiles[i].Name == profile.Name {
			m.config.Profiles[i] = profile
			changed = true
			break
		}
	}
	if !changed {
		// Not found, add new
		m.config.Profiles = append(m.config.Profiles, profile)
	}
	m.mu.Unlock()
	m.notifyChanged()
}

// DeleteProfile removes a profile by name
func (m *Manager) DeleteProfile(name string) {
	m.mu.Lock()
	changed := false
	for i := range m.config.Profiles {
		if m.config.Profiles[i].Name == name {
			m.config.Profiles = append(m.config.Profiles[:i], m.config.Profiles[i+1:]...)
			changed = true
			break
		}
	}
	m.mu.Unlock()
	if changed {
		m.notifyChanged()
	}
}

// GetCurrentProfile returns the currently active profile
func (m *Manager) GetCurrentProfile() *Profile {
	m.mu.RLock()
	name := m.config.General.CurrentProfile
	m.mu.RUnlock()
	return m.GetProfile(name)
}

// SyncFromCoordinator pulls profiles from the host machine if in agent mode
func (m *Manager) SyncFromCoordinator() error {
	cfg := m.Get()

	if cfg.General.Role != "agent" || cfg.General.CoordinatorAddr == "" {
		return nil
	}

	url := fmt.Sprintf("http://%s/api/config", cfg.General.CoordinatorAddr)
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	if cfg.General.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.General.APIToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("coordinator returned status %d: %s", resp.StatusCode, string(body))
	}

	var remoteConfig Config
	if err := json.NewDecoder(resp.Body).Decode(&remoteConfig); err != nil {
		return err
	}

	if len(remoteConfig.Profiles) > 0 {
		m.mu.Lock()
		m.config.Profiles = cloneProfiles(remoteConfig.Profiles)
		m.mu.Unlock()
		m.notifyChanged()
	}

	return m.Save()
}

// UpdateProfilesFromRemote updates profiles from a generic interface (decoded from JSON)
func (m *Manager) UpdateProfilesFromRemote(profiles interface{}) error {
	// Re-marshal to bytes then unmarshal to []Profile to be safe with types
	data, err := json.Marshal(profiles)
	if err != nil {
		return err
	}

	var newProfiles []Profile
	if err := json.Unmarshal(data, &newProfiles); err != nil {
		return err
	}

	m.mu.Lock()
	m.config.Profiles = cloneProfiles(newProfiles)
	m.mu.Unlock()
	m.notifyChanged()

	return m.Save()
}

// UpdateUSBForwardingFromHost updates the USB forwarding setting from Host
func (m *Manager) UpdateUSBForwardingFromHost(enabled bool) {
	m.mu.Lock()
	if m.config.General.USBForwardingEnabled == enabled {
		m.mu.Unlock()
		return
	}
	m.config.General.USBForwardingEnabled = enabled
	m.mu.Unlock()
	log.Printf("Config: Updated USB forwarding from Host: %v", enabled)
	m.notifyChanged()
}

func cloneConfig(in *Config) *Config {
	if in == nil {
		return nil
	}
	out := *in
	out.Profiles = cloneProfiles(in.Profiles)
	out.Monitors = append([]MonitorInfo(nil), in.Monitors...)
	out.General.PairedDevices = append([]PairedDevice(nil), in.General.PairedDevices...)
	if in.General.DeviceCalibrations != nil {
		out.General.DeviceCalibrations = make(map[string]DeviceCalibrationConfig, len(in.General.DeviceCalibrations))
		for id, calibration := range in.General.DeviceCalibrations {
			if calibration.NaturalScrolling != nil {
				value := *calibration.NaturalScrolling
				calibration.NaturalScrolling = &value
			}
			out.General.DeviceCalibrations[id] = calibration
		}
	}
	return &out
}

// IsPaired reports whether a peer identity is explicitly trusted. Empty IDs
// are never accepted when pairing is required. When pairing is disabled the
// token remains the sole legacy trust boundary.
func (m *Manager) IsPaired(deviceID string) bool {
	cfg := m.Get()
	if !cfg.General.PairingRequired {
		return deviceID != ""
	}
	if deviceID == "" {
		return false
	}
	for _, peer := range cfg.General.PairedDevices {
		if peer.Enabled && peer.DeviceID == deviceID {
			return true
		}
	}
	return false
}

// PairedPublicKey returns the optional public-key credential bound to a
// paired identity. Empty means the pairing is in token-only migration mode.
func (m *Manager) PairedPublicKey(deviceID string) string {
	cfg := m.Get()
	for _, peer := range cfg.General.PairedDevices {
		if peer.Enabled && peer.DeviceID == deviceID {
			return peer.PublicKey
		}
	}
	return ""
}

func (m *Manager) PairedRole(deviceID string) string {
	cfg := m.Get()
	for _, peer := range cfg.General.PairedDevices {
		if peer.Enabled && peer.DeviceID == deviceID {
			return peer.Role
		}
	}
	return ""
}

// PairDevice adds or updates an explicitly trusted peer. It returns an error
// for malformed identities so callers cannot accidentally create a wildcard.
func (m *Manager) PairDevice(peer PairedDevice) error {
	if peer.DeviceID == "" || len(peer.DeviceID) > 128 {
		return fmt.Errorf("device_id must be between 1 and 128 bytes")
	}
	if peer.PublicKey != "" && !security.ValidPublicKey(peer.PublicKey) {
		return fmt.Errorf("public_key is not a valid ed25519 public key")
	}
	peer.Enabled = true
	if peer.AddedAt.IsZero() {
		peer.AddedAt = time.Now().UTC()
	}
	m.mu.Lock()
	found := false
	for i := range m.config.General.PairedDevices {
		if m.config.General.PairedDevices[i].DeviceID == peer.DeviceID {
			if peer.AddedAt.IsZero() {
				peer.AddedAt = m.config.General.PairedDevices[i].AddedAt
			}
			m.config.General.PairedDevices[i] = peer
			found = true
			break
		}
	}
	if !found {
		m.config.General.PairedDevices = append(m.config.General.PairedDevices, peer)
	}
	m.mu.Unlock()
	m.notifyChanged()
	return nil
}

// UnpairDevice removes a peer from the explicit allowlist.
func (m *Manager) UnpairDevice(deviceID string) bool {
	m.mu.Lock()
	for i := range m.config.General.PairedDevices {
		if m.config.General.PairedDevices[i].DeviceID == deviceID {
			m.config.General.PairedDevices = append(m.config.General.PairedDevices[:i], m.config.General.PairedDevices[i+1:]...)
			m.mu.Unlock()
			m.notifyChanged()
			return true
		}
	}
	m.mu.Unlock()
	return false
}

// UpdatePairedEndpoint records the last authenticated endpoint observed for a
// paired peer. It is intentionally limited to an existing enabled pairing so
// network discovery cannot silently create a new trust entry.
func (m *Manager) UpdatePairedEndpoint(deviceID, endpoint string, seenAt time.Time) bool {
	if deviceID == "" || endpoint == "" {
		return false
	}
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	m.mu.Lock()
	changed := false
	for i := range m.config.General.PairedDevices {
		peer := &m.config.General.PairedDevices[i]
		if peer.Enabled && peer.DeviceID == deviceID {
			if peer.LastKnownEndpoint != endpoint || !peer.LastSeenAt.Equal(seenAt) {
				peer.LastKnownEndpoint = endpoint
				peer.LastSeenAt = seenAt
				changed = true
			}
			break
		}
	}
	m.mu.Unlock()
	if changed {
		m.notifyChanged()
	}
	return changed
}

func cloneProfiles(in []Profile) []Profile {
	if in == nil {
		return nil
	}
	out := make([]Profile, len(in))
	for i, profile := range in {
		out[i] = cloneProfile(profile)
	}
	return out
}

func cloneProfile(in Profile) Profile {
	out := in
	if in.MonitorInputs != nil {
		out.MonitorInputs = make(map[string]int, len(in.MonitorInputs))
		for id, source := range in.MonitorInputs {
			out.MonitorInputs[id] = source
		}
	}
	out.RemoteHosts = append([]RemoteHost(nil), in.RemoteHosts...)
	return out
}
