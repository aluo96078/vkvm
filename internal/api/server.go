// Package api provides HTTP API server for remote switching control.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"vkvm/internal/autostart"
	"vkvm/internal/config"
	"vkvm/internal/input"
	"vkvm/internal/network"
	"vkvm/internal/protocol"
	"vkvm/internal/security"
	"vkvm/internal/service"
	"vkvm/internal/switcher"
)

// Server provides HTTP API for remote control
type Server struct {
	configMgr              *config.Manager
	switcher               *switcher.Switcher
	token                  string
	tokens                 *security.KeyRing
	wsMgr                  *WSManager
	lastUSBForwardingState bool // Track last USB forwarding state for change detection
	mu                     sync.Mutex
	wsMu                   sync.RWMutex
	httpServer             *http.Server
	listener               net.Listener
	metrics                *input.Metrics
	mdnsAdvertiser         *network.MDNSAdvertiser
	permissionProvider     func() input.PermissionStatus
	serviceStatusProvider  func() service.DiagnosticStatus
	inputStateProvider     func() input.StateSnapshot
	udpTransportSelector   func(deviceID string) bool
}

// NewServer creates a new API server
func NewServer(configMgr *config.Manager, sw *switcher.Switcher) *Server {
	s := &Server{
		configMgr: configMgr,
		switcher:  sw,
		tokens:    security.NewKeyRing(configMgr.Get().General.APIToken, 30*time.Second),
	}
	s.wsMgr = newWSManager(s)

	// Initialize last USB forwarding state
	cfg := configMgr.Get()
	s.lastUSBForwardingState = cfg.General.USBForwardingEnabled

	// Register callback for USB forwarding changes
	configMgr.RegisterChangeCallback(func() {
		cfg := configMgr.Get()
		s.mu.Lock()
		changed := cfg.General.USBForwardingEnabled != s.lastUSBForwardingState
		previous := s.lastUSBForwardingState
		if changed {
			s.lastUSBForwardingState = cfg.General.USBForwardingEnabled
		}
		s.mu.Unlock()
		if s.tokens != nil {
			s.tokens.Set(cfg.General.APIToken)
		}
		if changed {
			log.Printf("API Server: USB forwarding setting changed from %v to %v, broadcasting to agents", previous, cfg.General.USBForwardingEnabled)
			if manager := s.currentWSManager(); manager != nil {
				manager.BroadcastUSBForwarding(cfg.General.USBForwardingEnabled)
			}
		}
	})

	return s
}

// BroadcastInput broadcasts input events to all connected agents
func (s *Server) BroadcastInput(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
	if manager := s.currentWSManager(); manager != nil {
		manager.BroadcastInput(eventType, deltaX, deltaY, button, pressed, keyCode, modifiers, wheelDelta, timestamp)
	}
}

// BroadcastInputEvent broadcasts a metadata-rich input event.
func (s *Server) BroadcastInputEvent(event input.InputEvent) {
	if manager := s.currentWSManager(); manager != nil {
		manager.BroadcastInputEvent(event)
	}
}

// BroadcastReleaseAll releases remote input state on every connected Agent.
func (s *Server) BroadcastReleaseAll() {
	if manager := s.currentWSManager(); manager != nil {
		manager.BroadcastReleaseAll()
	}
}

// SetInputMetrics attaches optional input-path diagnostics to /api/metrics.
func (s *Server) SetInputMetrics(metrics *input.Metrics) {
	s.mu.Lock()
	s.metrics = metrics
	s.mu.Unlock()
}

func (s *Server) SetInputStateProvider(provider func() input.StateSnapshot) {
	s.wsMu.Lock()
	s.inputStateProvider = provider
	manager := s.wsMgr
	s.wsMu.Unlock()
	if manager != nil {
		manager.SetInputStateProvider(provider)
	}
}

// SetPermissionProvider attaches platform TCC/permission diagnostics without
// making the API server itself responsible for prompting or mutating consent.
func (s *Server) SetPermissionProvider(provider func() input.PermissionStatus) {
	s.mu.Lock()
	s.permissionProvider = provider
	s.mu.Unlock()
}

func (s *Server) SetServiceStatusProvider(provider func() service.DiagnosticStatus) {
	s.mu.Lock()
	s.serviceStatusProvider = provider
	s.mu.Unlock()
}

func (s *Server) SetUDPTransportSelector(selector func(deviceID string) bool) {
	s.wsMu.Lock()
	s.udpTransportSelector = selector
	manager := s.wsMgr
	s.wsMu.Unlock()
	if manager != nil {
		manager.SetUDPTransportSelector(selector)
	}
}

func (s *Server) currentWSManager() *WSManager {
	s.wsMu.RLock()
	defer s.wsMu.RUnlock()
	return s.wsMgr
}

// prepareWSManager returns a stopped-or-new manager and starts its hub. The
// API supervisor may restart a Server after a transient listener failure, so
// the manager must not be permanently consumed by a sync.Once.
func (s *Server) prepareWSManager() *WSManager {
	s.wsMu.Lock()
	manager := s.wsMgr
	if manager == nil || manager.isStopped() {
		manager = newWSManager(s)
		manager.SetInputStateProvider(s.inputStateProvider)
		manager.SetUDPTransportSelector(s.udpTransportSelector)
		s.wsMgr = manager
	}
	s.wsMu.Unlock()
	manager.startAsync()
	return manager
}

// Start starts the API server on the specified port
func (s *Server) Start(port int) error {
	cfg := s.configMgr.Get()
	s.token = cfg.General.APIToken
	if cfg.General.APIToken == "" {
		return fmt.Errorf("API requires a non-empty authentication token")
	}

	// Start WebSocket Manager. This is deliberately restartable because the
	// service supervisor can recycle the HTTP listener after a transient error.
	wsMgr := s.prepareWSManager()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/switch", s.handleSwitch)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/permissions", s.handlePermissions)
	mux.HandleFunc("/api/service", s.handleServiceStatus)
	mux.HandleFunc("/api/discover", s.handleDiscover)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/pairing", s.handlePairing)
	mux.HandleFunc("/api/detect", s.handleDetect)
	mux.HandleFunc("/ws", wsMgr.handleWebSocket)
	mux.HandleFunc("/health", s.handleHealth)

	// Bind the wildcard TCP listener so a dual-stack host can accept both A
	// and AAAA mDNS endpoints. The OS may still select IPv4-only behaviour on
	// a platform policy that disables v6; discovery keeps the IPv4 endpoint as
	// a fallback in that case.
	addr := fmt.Sprintf(":%d", port)

	// Diagnostic: Print all local IPs to console to help user verify
	log.Printf("--- Diagnostic: Network Interfaces ---")
	if ips, err := network.GetLocalIPs(); err == nil {
		for _, ip := range ips {
			log.Printf("  Found Local IPv4: %s", ip)
		}
	}
	log.Printf("--------------------------------------")

	log.Printf("Starting API server on dual-stack wildcard %s", addr)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		wsMgr.stop()
		log.Printf("ERROR: API server failed to listen on %s: %v", addr, err)
		log.Printf("Note: VKVM will continue running without remote switching support.")
		return err
	}

	server := &http.Server{
		Handler:           s.authMiddleware(s.recoverMiddleware(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.mu.Lock()
	s.httpServer = server
	s.listener = ln
	s.mu.Unlock()
	localIP, _ := network.GetLocalIP()
	localIPs, _ := network.GetLocalAddresses()
	advertiser, mdnsErr := network.StartMDNSAdvertiser(context.Background(), network.MDNSServiceInfo{
		DeviceID: cfg.General.DeviceID, Role: cfg.General.Role, Port: port,
		IP: net.ParseIP(localIP), IPs: localIPs, ProtocolVersion: protocol.ProtocolVersion,
		Capabilities: protocol.CapabilityInputMetadata | protocol.CapabilityReliableControl | protocol.CapabilityStateSnapshot | protocol.CapabilityWebSocket | protocol.CapabilityUDP,
		PublicKey:    cfg.General.IdentityPublicKey,
	})
	if mdnsErr != nil {
		log.Printf("mDNS advertiser unavailable: %v", mdnsErr)
	} else {
		s.mu.Lock()
		s.mdnsAdvertiser = advertiser
		s.mu.Unlock()
	}
	defer func() {
		wsMgr.stop()
		if mdnsErr == nil && advertiser != nil {
			_ = advertiser.Close()
		}
		s.mu.Lock()
		if s.httpServer == server {
			s.httpServer = nil
			s.listener = nil
		}
		s.mu.Unlock()
	}()

	// This is blocking
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("ERROR: API server stopped: %v", err)
		return err
	}
	return nil
}

// Healthy reports whether the API listener is currently serving. It is used by
// the core supervisor and intentionally does not perform a network self-call.
func (s *Server) Healthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.httpServer != nil && s.listener != nil
}

func (s *Server) isDevicePaired(deviceID string) bool {
	return s.configMgr.IsPaired(deviceID)
}

// handlePairing manages the explicit peer allowlist. The endpoint is itself
// protected by the API bearer token; it never accepts a blank or wildcard
// device identity.
func (s *Server) handlePairing(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.configMgr.Get()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"required": cfg.General.PairingRequired,
			"devices":  cfg.General.PairedDevices,
		})
	case http.MethodPost:
		var peer config.PairedDevice
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		if err := decoder.Decode(&peer); err != nil {
			http.Error(w, "invalid pairing data", http.StatusBadRequest)
			return
		}
		if err := s.configMgr.PairDevice(peer); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.configMgr.Save(); err != nil {
			http.Error(w, "failed to save pairing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "paired", "device_id": peer.DeviceID})
	case http.MethodDelete:
		deviceID := r.URL.Query().Get("device_id")
		if deviceID == "" {
			http.Error(w, "device_id is required", http.StatusBadRequest)
			return
		}
		if !s.configMgr.UnpairDevice(deviceID) {
			http.Error(w, "device not paired", http.StatusNotFound)
			return
		}
		if err := s.configMgr.Save(); err != nil {
			http.Error(w, "failed to save pairing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "unpaired", "device_id": deviceID})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Stop gracefully shuts down HTTP, WebSocket and UDP-control listeners owned
// by this server. It is safe to call when Start failed or more than once.
func (s *Server) Stop() error {
	// Best effort: tell connected agents to release before their sockets are
	// closed. The caller may also send an explicit release-all first.
	s.BroadcastReleaseAll()
	s.mu.Lock()
	server := s.httpServer
	listener := s.listener
	advertiser := s.mdnsAdvertiser
	s.mdnsAdvertiser = nil
	s.mu.Unlock()
	var err error
	if server != nil {
		err = server.Close()
	} else if listener != nil {
		err = listener.Close()
	}
	if manager := s.currentWSManager(); manager != nil {
		manager.stop()
	}
	if advertiser != nil {
		if closeErr := advertiser.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

// recoverMiddleware prevents panics from crashing the whole server
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("PANIC RECOV: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// authMiddleware checks API token if configured
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log every request for debugging
		log.Printf("API: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)

		// Skip auth for health check
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if s.tokens == nil || !s.tokens.MatchBearer(authHeader) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleSwitch handles POST /api/switch?profile=<name>
func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	profileName := r.URL.Query().Get("profile")
	if profileName == "" {
		http.Error(w, "Missing profile parameter", http.StatusBadRequest)
		return
	}

	propagateStr := r.URL.Query().Get("propagate")
	propagate := propagateStr != "false"

	log.Printf("API: Switching to profile '%s' (remote request from %s, propagate=%v)", profileName, r.RemoteAddr, propagate)

	// If propagate is false, we need to bypass the Agent -> Host forwarding in SwitchToProfile
	if !propagate {
		if err := s.switcher.SwitchLocalOnly(profileName); err != nil {
			log.Printf("API: Switch error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := s.switcher.SwitchToProfile(profileName); err != nil {
			log.Printf("API: Switch error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"profile": profileName,
	})
}

// handleConfig handles GET (read) and POST (update) for configuration
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		cfg := s.configMgr.Get()
		cfg.General.APIToken = ""
		cfg.General.IdentityPrivateKey = ""
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)

	case "POST":
		var newCfg config.Config
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&newCfg); err != nil {
			http.Error(w, "Invalid configuration data", http.StatusBadRequest)
			return
		}
		var extra interface{}
		if err := decoder.Decode(&extra); err != io.EOF {
			http.Error(w, "Invalid configuration data", http.StatusBadRequest)
			return
		}
		currentCfg := s.configMgr.Get()
		// The secret is never returned by GET. Preserve it on updates that
		// originate from a redacted snapshot.
		if newCfg.General.APIToken == "" {
			newCfg.General.APIToken = currentCfg.General.APIToken
		}
		if newCfg.General.IdentityPublicKey == "" {
			newCfg.General.IdentityPublicKey = currentCfg.General.IdentityPublicKey
		}
		if newCfg.General.IdentityPrivateKey == "" {
			newCfg.General.IdentityPrivateKey = currentCfg.General.IdentityPrivateKey
		}
		if newCfg.General.APIEnabled && newCfg.General.APIToken == "" {
			http.Error(w, "api_token is required when api_enabled is true", http.StatusBadRequest)
			return
		}
		change := config.ClassifyChange(currentCfg, &newCfg)

		log.Printf("API: Receiving configuration update from %s", r.RemoteAddr)

		// Update in-memory config and save to disk
		s.configMgr.Set(&newCfg)
		if err := s.configMgr.Save(); err != nil {
			log.Printf("API: Failed to save received config: %v", err)
			http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
			return
		}

		// Immediately broadcast USB forwarding change to connected agents
		log.Printf("API: Config POST received, broadcasting USB forwarding=%v to agents", newCfg.General.USBForwardingEnabled)
		if manager := s.currentWSManager(); manager != nil {
			manager.BroadcastUSBForwarding(newCfg.General.USBForwardingEnabled)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":              "ok",
			"applied_immediately": change.Immediate,
			"restart_required":    change.RestartRequired,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStatus handles GET /api/status
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	currentProfile := s.switcher.GetCurrentProfile()
	cfg := s.configMgr.Get()

	status := map[string]interface{}{
		"current_profile":   currentProfile,
		"profiles":          getProfileNames(cfg.Profiles),
		"role":              cfg.General.Role,
		"api_enabled":       cfg.General.APIEnabled,
		"usb_forwarding":    cfg.General.USBForwardingEnabled,
		"start_on_boot":     cfg.General.StartOnBoot,
		"autostart_enabled": autostart.IsEnabled(),
	}
	s.mu.Lock()
	permissionProvider := s.permissionProvider
	s.mu.Unlock()
	if permissionProvider != nil {
		status["permissions"] = permissionProvider()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	provider := s.permissionProvider
	s.mu.Unlock()
	status := input.PermissionStatus{}
	if provider != nil {
		status = provider()
	} else {
		status = input.CurrentPermissions()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleServiceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	provider := s.serviceStatusProvider
	s.mu.Unlock()
	status := service.DiagnosticStatus{}
	if provider != nil {
		status = provider()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	metrics := s.metrics
	s.mu.Unlock()
	if metrics == nil {
		http.Error(w, "metrics unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metrics.Snapshot())
}

// handleDetect handles GET /api/detect - detects current active profile by reading monitor inputs
func (s *Server) handleDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get current inputs for all monitors
	currentInputs, err := s.switcher.GetCurrentInputs()
	if err != nil {
		log.Printf("API: Failed to get current inputs: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Try to detect which profile is currently active
	detectedProfile, err := s.switcher.DetectActiveProfile()
	if err != nil {
		log.Printf("API: Failed to detect profile: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build response with monitor details
	monitors, _ := s.switcher.ListMonitors()
	monitorDetails := make([]map[string]interface{}, 0)
	for _, monitor := range monitors {
		if input, exists := currentInputs[monitor.ID]; exists {
			monitorDetails = append(monitorDetails, map[string]interface{}{
				"id":            monitor.ID,
				"name":          monitor.Name,
				"current_input": input.String(),
				"input_code":    int(input),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detected_profile": detectedProfile,
		"monitors":         monitorDetails,
		"saved_profile":    s.switcher.GetCurrentProfile(),
	})
}

// handleHealth handles GET /health (for monitoring)
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDiscover handles GET /api/discover - scans LAN for VKVM instances
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg := s.configMgr.Get()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	log.Printf("API: Starting bounded mDNS discovery (fallback port %d)", cfg.General.APIPort)

	hosts, err := network.DiscoverVKVM(ctx, cfg.General.APIPort)
	if err != nil {
		log.Printf("API: Scan error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("API: Found %d VKVM instance(s) on LAN", len(hosts))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(hosts)
}

// getProfileNames extracts profile names from profiles list
func getProfileNames(profiles []config.Profile) []string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names
}

func validBearerToken(header, token string) bool {
	if token == "" || len(header) != len("Bearer ")+len(token) {
		return false
	}
	expected := "Bearer " + token
	return subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

// BroadcastSwitch provides a public method to broadcast switch events
func (s *Server) BroadcastSwitch(profile string, origin string) {
	if manager := s.currentWSManager(); manager != nil {
		manager.BroadcastSwitch(profile, origin)
	}
}
