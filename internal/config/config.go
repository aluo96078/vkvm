// Package config provides configuration management for the KVM switcher.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
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

// GeneralConfig contains general application settings
type GeneralConfig struct {
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

	// APIToken is an optional authentication token for API requests
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

	// EscapeHotkey is the emergency hotkey to disable input capture (e.g. "Ctrl+Alt+Shift+Esc")
	EscapeHotkey string `json:"escape_hotkey,omitempty"`
}

// DefaultConfig returns a new Config with sensible defaults
func DefaultConfig() *Config {
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
			StartOnBoot:          false,
			StartMinimized:       true,
			ShowNotifications:    true,
			CurrentProfile:       "PC1",
			APIEnabled:           true, // Ensure API is on by default for remote usage
			APIPort:              18080,
			Role:                 "host",
			SettingsHotkey:       "Ctrl+Alt+S",
			InputCaptureEnabled:  false,
			USBForwardingEnabled: true, // Enable USB forwarding by default
			EscapeHotkey:         "Ctrl+Alt+Shift+Esc",
		},
	}
}

// Manager handles loading and saving configuration
type Manager struct {
	mu         sync.Mutex
	configPath string
	config     *Config
	onChanged  func()
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
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", err
	}

	return filepath.Join(configDir, "config.json"), nil
}

// Load reads the configuration from disk
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.configPath)
	if os.IsNotExist(err) {
		// No config file, use defaults
		return nil
	}
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, m.config); err != nil {
		return err
	}
	if m.onChanged != nil {
		m.onChanged()
	}
	return nil
}

// Save writes the configuration to disk
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return err
	}

	log.Printf("Config: Saving configuration to %s (%d bytes)", m.configPath, len(data))
	return os.WriteFile(m.configPath, data, 0644)
}

// Get returns the current configuration
func (m *Manager) Get() *Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

// Set updates the configuration
func (m *Manager) Set(config *Config) {
	m.mu.Lock()
	m.config = config
	m.mu.Unlock()
	if m.onChanged != nil {
		m.onChanged()
	}
}

// RegisterChangeCallback registers a function to be called when config changes
func (m *Manager) RegisterChangeCallback(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChanged = fn
}

// GetProfile returns a profile by name
func (m *Manager) GetProfile(name string) *Profile {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.config.Profiles {
		if m.config.Profiles[i].Name == name {
			return &m.config.Profiles[i]
		}
	}
	return nil
}

// SetProfile updates or adds a profile
func (m *Manager) SetProfile(profile Profile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.config.Profiles {
		if m.config.Profiles[i].Name == profile.Name {
			m.config.Profiles[i] = profile
			return
		}
	}
	// Not found, add new
	m.config.Profiles = append(m.config.Profiles, profile)
}

// DeleteProfile removes a profile by name
func (m *Manager) DeleteProfile(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.config.Profiles {
		if m.config.Profiles[i].Name == name {
			m.config.Profiles = append(m.config.Profiles[:i], m.config.Profiles[i+1:]...)
			return
		}
	}
}

// GetCurrentProfile returns the currently active profile
func (m *Manager) GetCurrentProfile() *Profile {
	return m.GetProfile(m.config.General.CurrentProfile)
}

// SyncFromCoordinator pulls profiles from the host machine if in agent mode
func (m *Manager) SyncFromCoordinator() error {
	m.mu.Lock()
	cfg := m.config
	m.mu.Unlock()

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

	m.mu.Lock()
	// Update profiles from remote
	if len(remoteConfig.Profiles) > 0 {
		m.config.Profiles = remoteConfig.Profiles
	}
	m.mu.Unlock()

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
	m.config.Profiles = newProfiles
	m.mu.Unlock()

	return m.Save()
}
