// Package api provides HTTP API server for remote switching control.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"

	"vkvm/internal/config"
	"vkvm/internal/network"
	"vkvm/internal/switcher"
)

// Server provides HTTP API for remote control
type Server struct {
	configMgr *config.Manager
	switcher  *switcher.Switcher
	token     string
	wsMgr     *WSManager
}

// NewServer creates a new API server
func NewServer(configMgr *config.Manager, sw *switcher.Switcher) *Server {
	s := &Server{
		configMgr: configMgr,
		switcher:  sw,
	}
	s.wsMgr = newWSManager(s)
	return s
}

// Start starts the API server on the specified port
func (s *Server) Start(port int) error {
	cfg := s.configMgr.Get()
	s.token = cfg.General.APIToken

	// Start WebSocket Manager
	go s.wsMgr.start()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/switch", s.handleSwitch)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/discover", s.handleDiscover)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/ws", s.wsMgr.handleWebSocket)
	mux.HandleFunc("/health", s.handleHealth)

	// Use "0.0.0.0:port" and explicitly use tcp4 to avoid IPv6-only binding issues on Windows
	addr := fmt.Sprintf("0.0.0.0:%d", port)

	// Diagnostic: Print all local IPs to console to help user verify
	log.Printf("--- Diagnostic: Network Interfaces ---")
	if ips, err := network.GetLocalIPs(); err == nil {
		for _, ip := range ips {
			log.Printf("  Found Local IPv4: %s", ip)
		}
	}
	log.Printf("--------------------------------------")

	log.Printf("Starting API server on FORCED IPv4 %s", addr)

	// Create a listener explicitly with tcp4
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		log.Printf("ERROR: API server failed to listen on %s: %v", addr, err)
		log.Printf("Note: VKVM will continue running without remote switching support.")
		return err
	}

	server := &http.Server{
		Handler: s.authMiddleware(s.recoverMiddleware(mux)),
	}

	// This is blocking
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("ERROR: API server stopped: %v", err)
		return err
	}
	return nil
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

		// If token is configured, verify it
		if s.token != "" {
			authHeader := r.Header.Get("Authorization")
			expectedAuth := "Bearer " + s.token

			if authHeader != expectedAuth {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)

	case "POST":
		var newCfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, "Invalid configuration data", http.StatusBadRequest)
			return
		}

		log.Printf("API: Receiving configuration update from %s", r.RemoteAddr)

		// Update in-memory config and save to disk
		s.configMgr.Set(&newCfg)
		if err := s.configMgr.Save(); err != nil {
			log.Printf("API: Failed to save received config: %v", err)
			http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"current_profile": currentProfile,
		"profiles":        getProfileNames(cfg.Profiles),
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
	log.Printf("API: Starting LAN scan on port %d", cfg.General.APIPort)

	hosts, err := network.ScanLAN(cfg.General.APIPort)
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

// BroadcastSwitch provides a public method to broadcast switch events
func (s *Server) BroadcastSwitch(profile string, origin string) {
	if s.wsMgr != nil {
		s.wsMgr.BroadcastSwitch(profile, origin)
	}
}
