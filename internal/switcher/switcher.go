// Package switcher provides the core KVM switching logic.
package switcher

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"vkvm/internal/config"
	"vkvm/internal/ddc"
	"vkvm/internal/osutils"
)

// Switcher coordinates monitor input switching
type Switcher struct {
	mu         sync.Mutex
	controller ddc.Controller
	configMgr  *config.Manager

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

	return &Switcher{
		controller: controller,
		configMgr:  configMgr,
	}, nil
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
		log.Printf("Switcher: Operating as Agent, forwarding switch request '%s' to Host %s", profileName, cfg.General.CoordinatorAddr)
		host := config.RemoteHost{
			Address:     cfg.General.CoordinatorAddr,
			ProfileName: profileName,
		}
		go s.sendRemoteSwitchRequest(host, cfg.General.APIToken, true)
		return nil
	}

	var lastErr error
	count := 0

	// Determine switch mode
	switchMode := profile.SwitchMode
	if switchMode == "" {
		switchMode = "both" // default
	}

	// Execute local DDC switch if mode allows
	if switchMode == "local" || switchMode == "both" {
		// Wake up the system first (simulates mouse movement)
		osutils.WakeUp()
		time.Sleep(100 * time.Millisecond) // Brief delay for system to wake

		// Get currently detected monitors for this machine to filter inputs
		activeMonitors, _ := s.controller.ListMonitors()
		activeIDs := make(map[string]bool)
		for _, m := range activeMonitors {
			activeIDs[m.ID] = true
		}

		for monitorID, inputSource := range profile.MonitorInputs {
			// Skip monitors not found on this machine (avoids errors from synced foreign configs)
			if !activeIDs[monitorID] {
				log.Printf("Switcher: Skipping monitor %s (not detected on this computer)", monitorID)
				continue
			}

			if count > 0 {
				// Small delay between monitors to avoid DDC bus contention
				time.Sleep(500 * time.Millisecond)
			}
			if err := s.controller.SetInputSource(monitorID, ddc.InputSource(inputSource)); err != nil {
				log.Printf("Failed to switch monitor %s: %v", monitorID, err)
				lastErr = err
			}
			count++
		}
	}

	// Save config
	cfg.General.CurrentProfile = profileName
	if err := s.configMgr.Save(); err != nil {
		log.Printf("Failed to save config: %v", err)
	}

	// Send remote switch requests (non-blocking) if mode allows
	// Only send remote notifications if forwarding/propagation is allowed
	if allowForward && (switchMode == "remote" || switchMode == "both") && len(profile.RemoteHosts) > 0 {
		for _, remote := range profile.RemoteHosts {
			// When acting as host, we tell remotes to NOT propagate further to avoid loops
			propagate := cfg.General.Role != "host"
			go s.sendRemoteSwitchRequest(remote, cfg.General.APIToken, propagate)
		}
	}

	if s.onSwitch != nil {
		s.onSwitch(profileName)
	}

	return lastErr
}

// SyncProfiles pulls最新的 profiles from the coordinator
func (s *Switcher) SyncProfiles() error {
	return s.configMgr.SyncFromCoordinator()
}

// sendRemoteSwitchRequest sends a switch request to a remote host
// sendRemoteSwitchRequest sends a switch request to a remote host
func (s *Switcher) sendRemoteSwitchRequest(remote config.RemoteHost, token string, propagate bool) {
	uStr := fmt.Sprintf("http://%s/api/switch?profile=%s", remote.Address, url.QueryEscape(remote.ProfileName))
	if !propagate {
		uStr += "&propagate=false"
	}

	req, err := http.NewRequest("POST", uStr, nil)
	if err != nil {
		log.Printf("Remote switch: failed to create request to %s: %v", remote.Address, err)
		return
	}

	// Add authorization if token is configured
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	log.Printf("Remote switch: sending request to %s (profile: %s)", remote.Address, remote.ProfileName)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Remote switch: failed to connect to %s: %v", remote.Address, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Remote switch: %s returned status %d", remote.Address, resp.StatusCode)
		return
	}

	log.Printf("Remote switch: successfully notified %s", remote.Address)
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
