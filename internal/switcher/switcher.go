// Package switcher provides the core KVM switching logic.
package switcher

import (
	"fmt"
	"log"
	"sync"
	"time"

	"vkvm/internal/config"
	"vkvm/internal/ddc"
	"vkvm/internal/network"
	"vkvm/internal/osutils"
)

// Switcher coordinates monitor input switching
type Switcher struct {
	mu         sync.Mutex
	controller ddc.Controller
	configMgr  *config.Manager
	wsClient   *network.WSClient

	// Callbacks for UI notifications
	onSwitch func(profileName string)
	onError  func(error)
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
	if cfg.General.Role == "agent" && cfg.General.CoordinatorAddr != "" {
		log.Printf("Switcher: Initializing WebSocket client to Host %s", cfg.General.CoordinatorAddr)
		s.wsClient = network.NewWSClient(cfg.General.CoordinatorAddr, cfg.General.APIToken)

		// Wire up callbacks
		s.wsClient.OnSwitch = func(profile string) {
			log.Printf("Switcher: Received remote switch command for '%s'", profile)
			if err := s.SwitchLocalOnly(profile); err != nil {
				log.Printf("Switcher: Remote switch execution failed: %v", err)
			}
		}

		s.wsClient.OnSync = func(profiles interface{}) {
			if err := s.configMgr.UpdateProfilesFromRemote(profiles); err != nil {
				log.Printf("Switcher: Config sync failed: %v", err)
			} else {
				log.Printf("Switcher: Config synced from Host")
			}
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

func (s *Switcher) SwitchToProfile(profileName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile := s.configMgr.GetProfile(profileName)
	if profile == nil {
		return fmt.Errorf("profile not found: %s", profileName)
	}

	return s.switchToProfileInternal(profile, profileName, true)
}

// SwitchLocalOnly switches local monitors only, bypassing agent forwarding or host propagation
func (s *Switcher) SwitchLocalOnly(profileName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
		if s.wsClient != nil {
			log.Printf("Switcher: Operating as Agent, forwarding switch request '%s' to Host via WebSocket", profileName)
			s.wsClient.SendSwitch(profileName)
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
		activeMonitors, _ := s.controller.ListMonitors()
		activeIDs := make(map[string]bool)
		for _, m := range activeMonitors {
			activeIDs[m.ID] = true
		}

		var wg sync.WaitGroup
		var errMu sync.Mutex

		for monitorID, inputSource := range profile.MonitorInputs {
			// Skip monitors not found on this machine (avoids errors from synced foreign configs)
			if !activeIDs[monitorID] {
				log.Printf("Switcher: Skipping monitor %s (not detected on this computer)", monitorID)
				continue
			}

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
	}

	// Save config
	cfg.General.CurrentProfile = profileName
	if err := s.configMgr.Save(); err != nil {
		log.Printf("Failed to save config: %v", err)
	}

	// Legacy RemoteHosts support is deprecated in favor of WebSocket broadcast
	// The WSManager in the API server will handle broadcasting via the OnSwitch callback
	if allowForward && len(profile.RemoteHosts) > 0 {
		log.Printf("Switcher: Note: 'remote_hosts' in config is ignored in WebSocket mode. Ensure agents are connected to Host.")
	}

	if s.onSwitch != nil {
		s.onSwitch(profileName)
	}

	return lastErr
}

// SyncProfiles triggers a sync request via WebSocket if connected
func (s *Switcher) SyncProfiles() error {
	// With WebSocket, sync is automatic/pushed, but we can manually request it
	if s.wsClient != nil {
		s.wsClient.SendSyncRequest()
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
	if s.wsClient == nil {
		return false
	}
	return s.wsClient.IsConnected()
}
