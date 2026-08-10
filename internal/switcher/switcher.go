// Package switcher provides the core KVM switching logic.
package switcher

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"vkvm/internal/config"
	"vkvm/internal/ddc"
	"vkvm/internal/network"
	"vkvm/internal/notify"
	"vkvm/internal/osutils"
	"vkvm/internal/protocol"
)

// Switcher coordinates monitor input switching
type Switcher struct {
	mu         sync.Mutex // callbacks and agent transport
	switchMu   sync.Mutex // serializes DDC/profile transactions
	controller ddc.Controller
	configMgr  *config.Manager
	wsClient   *network.WSClient

	// Callbacks for UI notifications
	onSwitch      func(profileName string)
	onError       func(error)
	onAgentSwitch func(profileName string)
}

// New creates a new Switcher instance
func New(configMgr *config.Manager) (*Switcher, error) {
	controller, err := ddc.NewController()
	if err != nil {
		return nil, fmt.Errorf("failed to create DDC controller: %w", err)
	}

	s := &Switcher{
		controller: controller,
		configMgr:  configMgr,
	}

	// Initialize WebSocket client if Agent
	cfg := configMgr.Get()
	if cfg.General.Role == "agent" {
		coordinatorAddr := cfg.General.CoordinatorAddr
		if coordinatorAddr == "" {
			for _, peer := range cfg.General.PairedDevices {
				if peer.Enabled && peer.Role == "host" && peer.LastKnownEndpoint != "" {
					coordinatorAddr = peer.LastKnownEndpoint
					break
				}
			}
		}
		log.Printf("Switcher: Initializing WebSocket client to Host %s", coordinatorAddr)
		s.wsClient = network.NewWSClient(coordinatorAddr, cfg.General.APIToken, cfg.General.DeviceID)
		s.wsClient.SetPublicKey(cfg.General.IdentityPublicKey)
		s.wsClient.SetRole(cfg.General.Role)
		for _, peer := range cfg.General.PairedDevices {
			if peer.Enabled && peer.Role == "host" {
				s.wsClient.SetExpectedPeerDeviceID(peer.DeviceID)
				break
			}
		}

		// Wire up callbacks
		s.wsClient.OnSwitch = func(profile string) {
			log.Printf("Switcher: Received remote switch command for '%s'", profile)
			if err := s.SwitchLocalOnly(profile); err != nil {
				log.Printf("Switcher: Remote switch execution failed: %v", err)
			}
			s.mu.Lock()
			onAgentSwitch := s.onAgentSwitch
			s.mu.Unlock()
			if onAgentSwitch != nil {
				onAgentSwitch(profile)
			}
		}

		s.wsClient.OnSync = func(profiles interface{}, usbForwardingEnabled bool) {
			if err := s.configMgr.UpdateProfilesFromRemote(profiles); err != nil {
				log.Printf("Switcher: Config sync failed: %v", err)
			} else {
				log.Printf("Switcher: Config synced from Host (USB forwarding: %v)", usbForwardingEnabled)
			}
			// Update USB forwarding setting from Host
			s.configMgr.UpdateUSBForwardingFromHost(usbForwardingEnabled)
		}

		s.wsClient.OnUSBForwardingUpdate = func(enabled bool) {
			log.Printf("Switcher: Received USB forwarding update from Host: %v", enabled)
			// Update USB forwarding setting from Host
			s.configMgr.UpdateUSBForwardingFromHost(enabled)
		}

		// Start client
		s.wsClient.Start()
	}

	return s, nil
}

// SetOnSwitch sets the callback for switch events
func (s *Switcher) SetOnSwitch(callback func(profileName string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSwitch = callback
}

// SetOnError sets the callback for error events
func (s *Switcher) SetOnError(callback func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onError = callback
}

// SetAgentInputHandler installs the input callback on the single agent
// WebSocket session owned by Switcher. This prevents runService from opening
// a second connection to the same coordinator.
func (s *Switcher) SetAgentInputHandler(callback func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.SetInputHandler(callback)
	}
}

// SetAgentInputEventHandler installs a metadata-preserving callback on the
// single authenticated agent session.
func (s *Switcher) SetAgentInputEventHandler(callback func(protocol.InputPayload)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.SetInputEventHandler(callback)
	}
}

func (s *Switcher) SetAgentInputStateHandler(callback func(protocol.InputStatePayload)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.SetInputStateHandler(callback)
	}
}

// SetAgentSwitchHandler registers a callback for remote switch events after
// the local DDC switch has been applied.
func (s *Switcher) SetAgentSwitchHandler(callback func(profileName string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onAgentSwitch = callback
}

// SetAgentReleaseHandler registers the callback used to clear remote input
// state after transport loss or an explicit release-all command.
func (s *Switcher) SetAgentReleaseHandler(callback func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.SetReleaseHandler(callback)
	}
}

// SetAgentToken rotates the credential used by the authenticated coordinator
// session on its next reconnect.
func (s *Switcher) SetAgentToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.SetToken(token)
	}
}

// SetAgentCoordinatorEndpoint changes the endpoint used by the authenticated
// agent session after network discovery. It does not tear down a healthy
// connection; a caller may follow it with NotifyAgentNetworkChange.
func (s *Switcher) SetAgentCoordinatorEndpoint(endpoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil && endpoint != "" {
		s.wsClient.SetHostAddr(endpoint)
	}
}

func (s *Switcher) SetAgentExpectedPeerDeviceID(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.SetExpectedPeerDeviceID(deviceID)
	}
}

func (s *Switcher) SetAgentTransportCapabilities(capabilities uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.SetCapabilities(capabilities)
	}
}

// EnableAgentNetworkAwareReconnect makes the agent wait for an explicit
// network fingerprint change after a failed coordinator connection.
func (s *Switcher) EnableAgentNetworkAwareReconnect(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.EnableNetworkAwareReconnect(enabled)
	}
}

// NotifyAgentNetworkChange wakes a dormant agent connection attempt.
func (s *Switcher) NotifyAgentNetworkChange() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient != nil {
		s.wsClient.NotifyNetworkChange()
	}
}

// AgentSessionID exposes the authenticated WS session for transport handover
// fencing. It returns an empty string before the first handshake.
func (s *Switcher) AgentSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wsClient == nil {
		return ""
	}
	return s.wsClient.SessionID()
}

// WaitForAgentConnection waits until the single authenticated agent session
// has completed its handshake. Discovery must not report Active merely because
// a TCP port accepted a connection; this closes the gap between endpoint
// probing and an actually usable control transport.
func (s *Switcher) WaitForAgentConnection(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	client := s.wsClient
	s.mu.Unlock()
	if client == nil {
		return fmt.Errorf("agent WebSocket client is not configured")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if client.IsConnected() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Switcher) SwitchToProfile(profileName string) error {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()

	profile := s.configMgr.GetProfile(profileName)
	if profile == nil {
		return fmt.Errorf("profile not found: %s", profileName)
	}

	return s.switchToProfileInternal(profile, profileName, true)
}

// SwitchLocalOnly switches local monitors only, bypassing agent forwarding or host propagation
func (s *Switcher) SwitchLocalOnly(profileName string) error {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()

	profile := s.configMgr.GetProfile(profileName)
	if profile == nil {
		return fmt.Errorf("profile not found: %s", profileName)
	}

	return s.switchToProfileInternal(profile, profileName, false)
}

func (s *Switcher) switchToProfileInternal(profile *config.Profile, profileName string, allowForward bool) error {
	cfg := s.configMgr.Get()

	// Handle Agent Role: Forward to Host instead of local execution
	if allowForward && cfg.General.Role == "agent" && cfg.General.CoordinatorAddr != "" {
		s.mu.Lock()
		client := s.wsClient
		s.mu.Unlock()
		if client != nil {
			log.Printf("Switcher: Operating as Agent, forwarding switch request '%s' to Host via WebSocket", profileName)
			client.SendSwitch(profileName)
		} else {
			log.Printf("Switcher: Error: Agent role but no WebSocket client available")
		}
		return nil
	}

	var lastErr error
	// count := 0

	// Determine switch mode
	switchMode := profile.SwitchMode
	if switchMode == "" {
		switchMode = "both" // default
	}

	// Wake up the system first (simulates mouse movement)
	// We do this unconditionally when a switch is triggered locally (or via remote command handled locally)
	osutils.WakeUp()
	time.Sleep(100 * time.Millisecond) // Brief delay for system to wake

	// Execute local DDC switch if mode allows
	if switchMode == "local" || switchMode == "both" {
		// Get currently detected monitors for this machine to filter inputs
		activeMonitors, err := s.controller.ListMonitors()
		if err != nil {
			return fmt.Errorf("failed to detect monitors before switching: %w", err)
		}
		activeIDs := make(map[string]bool)
		for _, m := range activeMonitors {
			activeIDs[m.ID] = true
		}

		var wg sync.WaitGroup
		var errMu sync.Mutex

		missing := 0
		for monitorID := range profile.MonitorInputs {
			if !activeIDs[monitorID] {
				missing++
			}
		}
		if len(profile.MonitorInputs) > 0 && missing == len(profile.MonitorInputs) {
			return fmt.Errorf("no configured monitors for profile %q are currently detected", profileName)
		}
		if missing > 0 {
			return fmt.Errorf("profile %q is missing %d configured monitor(s)", profileName, missing)
		}

		for monitorID, inputSource := range profile.MonitorInputs {

			wg.Add(1)
			go func(mid string, src int) {
				defer wg.Done()
				if err := s.controller.SetInputSource(mid, ddc.InputSource(src)); err != nil {
					log.Printf("Failed to switch monitor %s: %v", mid, err)
					errMu.Lock()
					lastErr = err
					errMu.Unlock()
				}
			}(monitorID, inputSource)
		}
		wg.Wait()
		if lastErr != nil {
			return fmt.Errorf("profile %q switch failed: %w", profileName, lastErr)
		}
	}

	// Commit the active profile only after all monitor writes have succeeded.
	// Keep the previous value so a persistence failure can roll back the
	// in-memory transaction as well.
	previousProfile := cfg.General.CurrentProfile
	s.configMgr.SetCurrentProfile(profileName)
	if err := s.configMgr.Save(); err != nil {
		log.Printf("Failed to save config: %v", err)
		s.configMgr.SetCurrentProfile(previousProfile)
		return fmt.Errorf("failed to persist active profile: %w", err)
	}

	// Legacy RemoteHosts support is deprecated in favor of WebSocket broadcast
	// The WSManager in the API server will handle broadcasting via the OnSwitch callback
	if allowForward && len(profile.RemoteHosts) > 0 {
		log.Printf("Switcher: Note: 'remote_hosts' in config is ignored in WebSocket mode. Ensure agents are connected to Host.")
	}

	// Send system notification only when explicitly enabled.
	if cfg.General.ShowNotifications {
		notify.SwitchNotify(profileName)
	}

	s.mu.Lock()
	onSwitch := s.onSwitch
	s.mu.Unlock()
	if onSwitch != nil {
		onSwitch(profileName)
	}

	return nil
}

// SyncProfiles triggers a sync request via WebSocket if connected
func (s *Switcher) SyncProfiles() error {
	// With WebSocket, sync is automatic/pushed, but we can manually request it
	s.mu.Lock()
	client := s.wsClient
	s.mu.Unlock()
	if client != nil {
		client.SendSyncRequest()
		return nil
	}
	return nil
}

// GetCurrentProfile returns the current profile name
func (s *Switcher) GetCurrentProfile() string {
	return s.configMgr.Get().General.CurrentProfile
}

// ListMonitors returns all detected monitors
func (s *Switcher) ListMonitors() ([]ddc.Monitor, error) {
	return s.controller.ListMonitors()
}

// TestMonitor tests switching a specific monitor to verify DDC works
func (s *Switcher) TestMonitor(monitorID string, input ddc.InputSource) error {
	return s.controller.SetInputSource(monitorID, input)
}

// IsConnectedToCheck returns true if the agent is connected to the host
func (s *Switcher) IsConnectedToCheck() bool {
	s.mu.Lock()
	client := s.wsClient
	s.mu.Unlock()
	if client == nil {
		return false
	}
	return client.IsConnected()
}

// AgentTransportState returns the authenticated coordinator transport state.
// Hosts report disconnected because they do not own an agent transport.
func (s *Switcher) AgentTransportState() network.ConnectionState {
	s.mu.Lock()
	client := s.wsClient
	s.mu.Unlock()
	if client == nil {
		return network.ConnectionDisconnected
	}
	return client.State()
}

// Close stops the agent transport, if one is running. Host instances have no
// transport and Close is a no-op.
func (s *Switcher) Close() {
	s.mu.Lock()
	client := s.wsClient
	s.wsClient = nil
	s.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

// GetCurrentInputs returns the current input source for all detected monitors
func (s *Switcher) GetCurrentInputs() (map[string]ddc.InputSource, error) {
	monitors, err := s.controller.ListMonitors()
	if err != nil {
		return nil, err
	}

	inputs := make(map[string]ddc.InputSource)
	for _, monitor := range monitors {
		input, err := s.controller.GetCurrentInput(monitor.ID)
		if err != nil {
			log.Printf("Failed to get current input for monitor %s: %v", monitor.ID, err)
			continue
		}
		inputs[monitor.ID] = input
	}

	return inputs, nil
}

// DetectActiveProfile attempts to detect which profile is currently active by comparing
// current monitor inputs with profile configurations
func (s *Switcher) DetectActiveProfile() (string, error) {
	currentInputs, err := s.GetCurrentInputs()
	if err != nil {
		return "", err
	}

	cfg := s.configMgr.Get()

	// Try to match current inputs with profiles
	for _, profile := range cfg.Profiles {
		matches := true

		// Check if all profile's monitor inputs match current state
		for monitorID, expectedInput := range profile.MonitorInputs {
			currentInput, exists := currentInputs[monitorID]
			if !exists {
				// A missing configured monitor is uncertainty, not a match. This
				// keeps profile-based input injection fail-closed during disconnects.
				matches = false
				break
			}

			if int(currentInput) != expectedInput {
				matches = false
				break
			}
		}

		if matches && len(profile.MonitorInputs) > 0 {
			return profile.Name, nil
		}
	}

	return "", nil // No matching profile found
}
