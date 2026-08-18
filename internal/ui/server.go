// Package ui provides the configuration user interface.
package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"vkvm/internal/autostart"
	"vkvm/internal/config"
	"vkvm/internal/ddc"
	"vkvm/internal/input"
	"vkvm/internal/network"
	"vkvm/internal/osutils"
	"vkvm/internal/switcher"
)

// Server provides a web-based configuration UI
type Server struct {
	configMgr       *config.Manager
	switcher        *switcher.Switcher
	listener        net.Listener
	onConfigSaved   func(*config.Config) error
	onSwitchProfile func(string) error
}

// NewServer creates a new UI server
func NewServer(cfgMgr *config.Manager, sw *switcher.Switcher) *Server {
	return &Server{
		configMgr: cfgMgr,
		switcher:  sw,
	}
}

// SetConfigSaveHandler lets a split companion forward an authenticated
// configuration update to the non-interactive core after saving its local
// snapshot. A nil handler keeps the standalone desktop behavior.
func (s *Server) SetConfigSaveHandler(handler func(*config.Config) error) {
	s.onConfigSaved = handler
}

// SetSwitchHandler lets a split companion route profile switches through the
// core instead of mutating a second, process-local switcher state.
func (s *Server) SetSwitchHandler(handler func(string) error) {
	s.onSwitchProfile = handler
}

// Start starts the UI server and opens the browser
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/monitors", s.handleMonitors)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/switch", s.handleSwitch)
	mux.HandleFunc("/api/test", s.handleTest)
	mux.HandleFunc("/api/discover", s.handleUIDiscover)
	mux.HandleFunc("/api/test-remote", s.handleTestRemote)
	mux.HandleFunc("/api/sync-to", s.handleSyncTo)
	mux.HandleFunc("/api/sleep-display", s.handleSleepDisplay)
	mux.HandleFunc("/api/connection-status", s.handleConnectionStatus)
	mux.HandleFunc("/api/permissions", s.handlePermissions)
	mux.HandleFunc("/api/autostart", s.handleAutostart)

	// Find an available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.listener = listener

	port := listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://127.0.0.1:%d", port)

	log.Printf("Starting UI server at %s", url)

	// Open browser
	go openBrowser(url)

	return http.Serve(listener, csrfMiddleware(mux))
}

// Stop stops the UI server
func (s *Server) Stop() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "darwin":
		err = exec.Command("open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		err = exec.Command("xdg-open", url).Start()
	}
	if err != nil {
		log.Printf("Failed to open browser: %v", err)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, nil)
}

func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	monitors, err := s.switcher.ListMonitors()
	if err != nil {
		// Return empty array instead of error for better UI handling
		monitors = []ddc.Monitor{}
	}

	// Ensure we never return null
	if monitors == nil {
		monitors = []ddc.Monitor{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(monitors)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "GET":
		cfg := s.configMgr.Get()
		cfg.General.APIToken = ""
		cfg.General.IdentityPrivateKey = ""
		json.NewEncoder(w).Encode(cfg)
	case "POST":
		var cfg config.Config
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var extra interface{}
		if err := decoder.Decode(&extra); err != io.EOF {
			http.Error(w, "invalid configuration data", http.StatusBadRequest)
			return
		}
		current := s.configMgr.Get()
		if cfg.General.APIToken == "" {
			cfg.General.APIToken = current.General.APIToken
		}
		if cfg.General.IdentityPublicKey == "" {
			cfg.General.IdentityPublicKey = current.General.IdentityPublicKey
		}
		if cfg.General.IdentityPrivateKey == "" {
			cfg.General.IdentityPrivateKey = current.General.IdentityPrivateKey
		}
		if cfg.General.APIEnabled && cfg.General.APIToken == "" {
			http.Error(w, "api_token is required when api_enabled is true", http.StatusBadRequest)
			return
		}
		change := config.ClassifyChange(current, &cfg)
		s.configMgr.Set(&cfg)
		if err := s.configMgr.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s.onConfigSaved != nil {
			if err := s.onConfigSaved(&cfg); err != nil {
				http.Error(w, fmt.Sprintf("core configuration sync failed: %v", err), http.StatusBadGateway)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":              "ok",
			"applied_immediately": change.Immediate,
			"restart_required":    change.RestartRequired,
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	profileName := r.URL.Query().Get("profile")
	if profileName == "" {
		http.Error(w, "Missing profile parameter", http.StatusBadRequest)
		return
	}

	if s.onSwitchProfile != nil {
		if err := s.onSwitchProfile(profileName); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	} else {
		if s.switcher == nil {
			http.Error(w, "switcher unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := s.switcher.SwitchToProfile(profileName); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	currentProfile := profileName
	if s.switcher != nil && s.onSwitchProfile == nil {
		currentProfile = s.switcher.GetCurrentProfile()
	}
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"profile": currentProfile,
	})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	monitorID := r.URL.Query().Get("monitor")
	inputStr := r.URL.Query().Get("input")

	input, err := strconv.Atoi(inputStr)
	if err != nil {
		http.Error(w, "Invalid input value", http.StatusBadRequest)
		return
	}

	if err := s.switcher.TestMonitor(monitorID, ddc.InputSource(input)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleUIDiscover(w http.ResponseWriter, r *http.Request) {
	cfg := s.configMgr.Get()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	hosts, err := network.DiscoverVKVM(ctx, cfg.General.APIPort)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(hosts)
}

// handleTestRemote tests connectivity to a remote VKVM instance
func (s *Server) handleTestRemote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	addr := r.URL.Query().Get("addr")
	if addr == "" {
		http.Error(w, "Missing addr", http.StatusBadRequest)
		return
	}

	validatedAddr, err := validateRemoteAddress(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("UI: Testing remote host %s", validatedAddr)

	client := &http.Client{
		Timeout: 2 * time.Second,
	}

	resp, err := client.Get("http://" + validatedAddr + "/health")
	if err != nil {
		log.Printf("UI: Test failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Remote returned status %d", resp.StatusCode), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

// handleSleepDisplay turns off the display
func (s *Server) handleSleepDisplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Printf("UI: Requested display sleep")
	if err := osutils.TurnOffDisplay(); err != nil {
		log.Printf("Display sleep failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

// handleSyncTo pushes local config to a remote VKVM instance
func (s *Server) handleSyncTo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	addr := r.URL.Query().Get("addr")
	if addr == "" {
		http.Error(w, "Missing addr", http.StatusBadRequest)
		return
	}

	validatedAddr, err := validateRemoteAddress(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("UI: Syncing local config to %s", validatedAddr)

	cfg := s.configMgr.Get()
	// Never send the local API secret as part of a configuration sync.
	cfg.General.APIToken = ""
	// The identity private key is local-only; the peer only needs its public key
	// through an explicit pairing operation.
	cfg.General.IdentityPrivateKey = ""
	data, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, "Failed to encode config", http.StatusInternalServerError)
		return
	}

	// Create request to target machine's Remote API
	token := r.URL.Query().Get("token")
	if token == "" {
		token = s.configMgr.Get().General.APIToken
	}
	targetURL := fmt.Sprintf("http://%s/api/config", validatedAddr)

	req, err := http.NewRequest("POST", targetURL, bytes.NewBuffer(data))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("UI: Sync failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Target returned status %d", resp.StatusCode), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

func (s *Server) handleConnectionStatus(w http.ResponseWriter, r *http.Request) {
	connected := s.switcher.IsConnectedToCheck()
	state := s.switcher.AgentTransportState()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"connected": connected,
		"state":     state,
	})
}

func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(input.CurrentPermissions())
}

func (s *Server) handleAutostart(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"enabled": autostart.IsEnabled(), "requested": s.configMgr.Get().General.StartOnBoot})
	case http.MethodPost:
		var request struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
			http.Error(w, "invalid autostart request", http.StatusBadRequest)
			return
		}
		var err error
		if request.Enabled {
			err = autostart.Enable()
		} else {
			err = autostart.Disable()
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cfg := s.configMgr.Get()
		cfg.General.StartOnBoot = request.Enabled
		s.configMgr.Set(cfg)
		if err := s.configMgr.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "enabled": autostart.IsEnabled()})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			origin := r.Header.Get("Origin")
			if origin == "" {
				origin = r.Header.Get("Referer")
			}
			u, err := url.Parse(origin)
			if err != nil || u.Host == "" || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				http.Error(w, "CSRF validation failed", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func validateRemoteAddress(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return "", fmt.Errorf("addr must be an explicit host:port")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", fmt.Errorf("invalid remote port")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("remote host must be a numeric IP address")
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return "", fmt.Errorf("remote host is not an allowed LAN address")
	}
	if !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !isSharedAddress(ip) {
		return "", fmt.Errorf("remote host must be on a private, link-local, or shared LAN")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(parsedPort)), nil
}

func isSharedAddress(ip net.IP) bool {
	ip = ip.To4()
	return ip != nil && ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127
}

var tmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="zh-TW">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>VKVM Settings</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'SF Pro Display', 'Segoe UI', Roboto, sans-serif;
            background: linear-gradient(135deg, #1a1a2e 0%, #16213e 100%);
            color: #e2e8f0;
            min-height: 100vh;
            padding: 2rem;
        }
        .container { max-width: 900px; margin: 0 auto; }
        h1 {
            font-size: 2rem;
            font-weight: 700;
            margin-bottom: 2rem;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }
        .card {
            background: rgba(255,255,255,0.05);
            backdrop-filter: blur(20px);
            border: 1px solid rgba(255,255,255,0.1);
            border-radius: 16px;
            padding: 1.5rem;
            margin-bottom: 1.5rem;
        }
        .card h2 {
            font-size: 1.25rem;
            margin-bottom: 1rem;
            color: #a5b4fc;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        .profile-item {
            background: rgba(255,255,255,0.03);
            border-radius: 12px;
            padding: 1rem;
            margin-bottom: 0.75rem;
        }
        .profile-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 0.5rem;
        }
        .profile-name {
            font-size: 1.1rem;
            font-weight: 600;
        }
        .profile-hotkey {
            background: rgba(102,126,234,0.2);
            padding: 0.25rem 0.75rem;
            border-radius: 6px;
            font-size: 0.875rem;
            color: #a5b4fc;
        }
        .monitor-inputs {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
            gap: 0.5rem;
            margin-top: 0.75rem;
        }
        .input-group {
            display: flex;
            align-items: center;
            gap: 0.5rem;
        }
        .input-group label { font-size: 0.875rem; color: #94a3b8; }
        select, input[type="text"] {
            background: rgba(255,255,255,0.1);
            border: 1px solid rgba(255,255,255,0.2);
            border-radius: 8px;
            padding: 0.5rem;
            color: #e2e8f0;
            font-size: 0.875rem;
            flex: 1;
        }
        select:focus, input:focus { outline: none; border-color: #667eea; }
        .btn {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            border: none;
            border-radius: 8px;
            padding: 0.75rem 1.5rem;
            color: white;
            font-weight: 600;
            cursor: pointer;
            transition: transform 0.2s, box-shadow 0.2s;
            font-size: 0.875rem;
        }
        .btn-warning {
             background: linear-gradient(135deg, #f6d365 0%, #fda085 100%);
             color: #1e293b;
        }
        .btn:hover { transform: translateY(-2px); box-shadow: 0 4px 20px rgba(102,126,234,0.4); }
        .btn-small {
            padding: 0.4rem 0.8rem;
            font-size: 0.8rem;
        }
        .btn-secondary {
            background: rgba(255,255,255,0.1);
            border: 1px solid rgba(255,255,255,0.2);
        }
        .btn-danger {
            background: rgba(239,68,68,0.8);
        }
        #status-bar {
            position: fixed;
            bottom: 2rem;
            right: 2rem;
            padding: 1rem 1.5rem;
            background: rgba(0,0,0,0.9);
            border-radius: 12px;
            display: none;
            color: white;
        }
        .action-btns {
            display: flex;
            gap: 0.5rem;
        }
        .hotkey-recorder-overlay {
            position: fixed;
            top: 0; left: 0; right: 0; bottom: 0;
            background: rgba(0, 0, 0, 0.85);
            backdrop-filter: blur(8px);
            z-index: 1000;
            display: none;
            flex-direction: column;
            align-items: center;
            justify-content: center;
            color: #fff;
        }
        .recorder-box {
            background: rgba(255, 255, 255, 0.05);
            border: 2px dashed #4f46e5;
            border-radius: 20px;
            padding: 3rem;
            text-align: center;
            min-width: 400px;
        }
        .recorded-keys {
            font-size: 2.5rem;
            font-weight: 800;
            margin: 2rem 0;
            color: #818cf8;
            min-height: 4rem;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>⌨️ VKVM Settings</h1>

        <div class="card">
            <h2>General Settings</h2>
            <div class="input-grid" style="display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-bottom: 0.75rem;">
                <div class="input-group" style="flex-direction: row; align-items: center; gap: 0.5rem;">
                    <input type="checkbox" id="api-enabled" onchange="updateGeneralConfig()">
                    <label style="margin: 0; cursor: pointer;">Enable API Server (Remote Control)</label>
                </div>
                <div class="input-group">
                    <label>API Port:</label>
                    <input type="text" id="api-port" onchange="updateGeneralConfig()" placeholder="18080">
                </div>
            </div>
            <div class="input-grid" style="display: grid; grid-template-columns: 1fr 1fr; gap: 1rem;">
                <div class="input-group">
                    <label>This Computer IP:</label>
                    <input type="text" id="this-computer-ip" onchange="updateGeneralConfig()" placeholder="Auto-detecting...">
                </div>
                <div class="input-group" style="flex-direction: row; align-items: center; gap: 0.5rem;">
                    <input type="checkbox" id="start-on-boot" onchange="updateGeneralConfig()">
                    <label style="margin: 0; cursor: pointer;">Start on Boot</label>
                </div>
            </div>
            <div class="input-grid" style="display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 0.75rem;">
                <div class="input-group">
                    <label>Settings Hotkey:</label>
                    <div style="display: flex; gap: 0.5rem;">
                        <input type="text" id="settings-hotkey" onchange="updateGeneralConfig()" placeholder="Ctrl+Alt+S" style="flex: 1;">
                        <button class="btn btn-small" style="background: #ef4444;" onclick="startRecording('settings')">🔴 Record</button>
                    </div>
                </div>
                <div class="input-group">
                    <label>Sleep Hotkey:</label>
                    <div style="display: flex; gap: 0.5rem;">
                        <input type="text" id="sleep-hotkey" onchange="updateGeneralConfig()" placeholder="Ctrl+Alt+P" style="flex: 1;">
                        <button class="btn btn-small" style="background: #ef4444;" onclick="startRecording('sleep')">🔴 Record</button>
                    </div>
                </div>
                <div class="input-group">
                     <label>Power control:</label>
                     <button class="btn btn-small btn-warning" onclick="sleepDisplay()">💤 Sleep Displays</button>
                </div>
            </div>
            </div>
            <div class="input-grid" style="display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 0.75rem; border-top: 1px solid rgba(255,255,255,0.05); padding-top: 0.75rem;">
                <div class="input-group">
                    <label>Machine Role:</label>
                    <select id="role" onchange="updateGeneralConfig(); toggleCoordinatorUI()">
                        <option value="host">Host (Master - Controls Others)</option>
                        <option value="agent">Agent (Slave - Follows Host)</option>
                    </select>
                </div>
                <div class="input-group" id="coordinator-group">
                    <label>Coordinator Address (IP:Port):</label>
                    <div style="display: flex; gap: 0.5rem; align-items: center;">
                        <input type="text" id="coordinator-addr" onchange="updateGeneralConfig()" placeholder="e.g. 192.168.1.50:18080" style="flex: 1;">
                        <div id="connection-status" style="display: none; padding: 0.5rem 1rem; border-radius: 8px; font-size: 0.875rem; font-weight: 600;">
                            Checking...
                        </div>
                    </div>
                </div>
            </div>

            <!-- Agent-specific settings -->
            <div id="agent-settings" style="display: none; margin-top: 0.75rem; border-top: 1px solid rgba(255,255,255,0.05); padding-top: 0.75rem;">
                <div class="input-group">
                    <label>Agent Profile (Auto-detect Display):</label>
                    <input type="text" id="agent-profile" onchange="updateGeneralConfig()" placeholder="e.g. Mac (leave empty to disable auto-detect)">
                    <small style="color: #94a3b8; font-size: 0.875rem; margin-top: 0.25rem;">
                        When set, input injection will only work when display shows this profile
                    </small>
                </div>
            </div>

            <!-- Host-specific settings -->
            <div id="host-settings" style="display: none; margin-top: 0.75rem; border-top: 1px solid rgba(255,255,255,0.05); padding-top: 0.75rem;">
                <div class="input-group" style="flex-direction: row; align-items: center; gap: 0.5rem; margin-bottom: 1rem;">
                    <input type="checkbox" id="usb-forwarding-enabled" onchange="updateGeneralConfig()">
                    <label style="margin: 0; cursor: pointer;">Enable USB Input Forwarding (Keyboard/Mouse)</label>
                </div>
                <small style="color: #94a3b8; font-size: 0.875rem; margin-bottom: 1rem; display: block;">
                    This setting is synced to all connected Agents
                </small>
                <div class="input-group" style="flex-direction: row; align-items: center; gap: 0.5rem; margin-bottom: 1rem;">
                    <input type="checkbox" id="input-capture-enabled" onchange="updateGeneralConfig()">
                    <label style="margin: 0; cursor: pointer;">Enable Input Capture Mode (Block local keyboard/mouse)</label>
                </div>
                <div class="input-group">
                    <label>Emergency Escape Hotkey:</label>
                    <div style="display: flex; gap: 0.5rem;">
                        <input type="text" id="escape-hotkey" onchange="updateGeneralConfig()" placeholder="Ctrl+Alt+Shift+Esc" style="flex: 1;">
                        <button class="btn btn-small" style="background: #ef4444;" onclick="startRecording('escape')">🔴 Record</button>
                    </div>
                    <small style="color: #94a3b8; font-size: 0.875rem; margin-top: 0.25rem;">
                        Press this to force disable input capture and regain control
                    </small>
                </div>
            </div>
            </div>
        </div>

        <div class="card">
            <h2>
                Profiles
                <button class="btn btn-small" id="add-profile-btn" onclick="addProfile()">+ Add Profile</button>
            </h2>
            <div id="agent-sync-notice" style="display: none; background: rgba(102,126,234,0.1); border: 1px solid rgba(102,126,234,0.2); border-radius: 8px; padding: 0.5rem; margin-bottom: 1rem; color: #a5b4fc; font-size: 0.875rem;">
                ℹ️ This machine is an <strong>Agent</strong>. Profiles are synced from the Host and are read-only.
            </div>
            <div id="profiles-list"></div>
        </div>

        <div id="hotkey-recorder" class="hotkey-recorder-overlay">
            <div class="recorder-box">
                <h2 style="color: #fff; margin-bottom: 1rem;">Recording Hotkey...</h2>
                <p style="color: #94a3b8; margin-bottom: 2rem;">Press any key combination or mouse button</p>
                <div id="recorded-display" class="recorded-keys">Press Keys...</div>
                <div style="display: flex; gap: 1rem; justify-content: center;">
                    <button class="btn btn-secondary" onclick="cancelRecording()">Cancel</button>
                    <button class="btn" style="background: #4f46e5;" onclick="saveRecording()">Done</button>
                </div>
                <p style="margin-top: 2rem; font-size: 0.8rem; color: #64748b;">(Supports Ctrl, Alt, Shift, Cmd, and Mouse Side Buttons)</p>
            </div>
        </div>

        <div class="card">
            <h2>Detected Monitors</h2>
            <div id="monitors-info"></div>
        </div>

        <div class="card">
            <h2>
                Network Discovery
                <button class="btn btn-small btn-secondary" onclick="scanNetwork()">Scan LAN</button>
            </h2>
            <div id="discovery-list" style="margin-top: 1rem;">
                <p style="color: #94a3b8;">Click "Scan LAN" to find other computers running VKVM.</p>
            </div>
        </div>

        <button class="btn" onclick="saveConfig()">💾 Save Settings</button>
    </div>

    <div id="status-bar"></div>

    <script>
        let config = null;
        let monitors = [];

        function escapeHtml(value) {
            return String(value ?? '').replace(/[&<>"']/g, ch => ({
                '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
            }[ch]));
        }

        function jsArg(value) {
            return escapeHtml(JSON.stringify(String(value ?? '')));
        }

        async function loadData() {
            try {
                const [cfgRes, monRes] = await Promise.all([
                    fetch('/api/config'),
                    fetch('/api/monitors')
                ]);
                config = await cfgRes.json();
                monitors = await monRes.json() || [];
                // Ensure arrays exist
                if (!config.profiles) config.profiles = [];
                if (!config.monitors) config.monitors = [];
                renderUI();
            } catch (e) {
                showStatus('Error loading data: ' + e.message, true);
            }
        }

        function renderUI() {
            renderGeneral();
            renderProfiles();
            renderMonitors();
            checkConnectionStatus();
            
            // Start polling status if agent
            setInterval(checkConnectionStatus, 3000);
        }

        async function checkConnectionStatus() {
            if (config.general.role !== 'agent') {
                document.getElementById('connection-status').style.display = 'none';
                return;
            }
            
            try {
                const res = await fetch('/api/connection-status');
                const data = await res.json();
                const el = document.getElementById('connection-status');
                el.style.display = 'block';
                
                if (data.connected) {
                    el.style.background = 'rgba(16, 185, 129, 0.2)';
                    el.style.color = '#34d399';
                    el.style.border = '1px solid rgba(16, 185, 129, 0.3)';
                    el.innerHTML = '✅ Connected to Host';
                } else if (data.state === 'waiting_for_network_change') {
                    el.style.background = 'rgba(245, 158, 11, 0.2)';
                    el.style.color = '#fbbf24';
                    el.style.border = '1px solid rgba(245, 158, 11, 0.3)';
                    el.innerHTML = '⏸ Waiting for network change';
                } else {
                    el.style.background = 'rgba(239, 68, 68, 0.2)';
                    el.style.color = '#f87171';
                    el.style.border = '1px solid rgba(239, 68, 68, 0.3)';
                    el.innerHTML = '❌ Disconnected';
                }
            } catch (e) {
                // Ignore errors
            }
        }

        function renderGeneral() {
            document.getElementById('api-enabled').checked = config.general.api_enabled;
            document.getElementById('api-port').value = config.general.api_port || 18080;
            document.getElementById('this-computer-ip').value = config.general.this_computer_ip || '';
            document.getElementById('start-on-boot').checked = config.general.start_on_boot;
            document.getElementById('settings-hotkey').value = config.general.settings_hotkey || 'Ctrl+Alt+S';
            document.getElementById('sleep-hotkey').value = config.general.sleep_hotkey || '';
            document.getElementById('role').value = config.general.role || 'host';
            document.getElementById('coordinator-addr').value = config.general.coordinator_addr || '';
            document.getElementById('agent-profile').value = config.general.agent_profile || '';
            document.getElementById('usb-forwarding-enabled').checked = config.general.usb_forwarding_enabled === true;
            document.getElementById('input-capture-enabled').checked = config.general.input_capture_enabled === true;
            document.getElementById('escape-hotkey').value = config.general.escape_hotkey || 'Ctrl+Alt+Shift+Esc';
            
            const isAgent = config.general.role === 'agent';
            document.getElementById('coordinator-group').style.visibility = isAgent ? 'visible' : 'hidden';
            document.getElementById('add-profile-btn').style.display = isAgent ? 'none' : 'inline-block';
            document.getElementById('agent-sync-notice').style.display = isAgent ? 'block' : 'none';
            document.getElementById('agent-settings').style.display = isAgent ? 'block' : 'none';
            document.getElementById('host-settings').style.display = isAgent ? 'none' : 'block';

            // Disable input capture checkbox if agent profile is set (dynamic control)
            const inputCaptureCheckbox = document.getElementById('input-capture-enabled');
            const agentProfile = config.general.agent_profile || '';
            if (!isAgent && agentProfile.trim() !== '') {
                inputCaptureCheckbox.disabled = true;
                inputCaptureCheckbox.title = 'Disabled: Input capture is controlled automatically based on active profile';
            } else {
                inputCaptureCheckbox.disabled = false;
                inputCaptureCheckbox.title = '';
            }
        }

        function toggleCoordinatorUI() {
            updateGeneralConfig(); // Refresh based on UI change
            renderGeneral(); // Re-render to show/hide parts
        }

        function updateGeneralConfig() {
            config.general.api_enabled = document.getElementById('api-enabled').checked;
            config.general.api_port = parseInt(document.getElementById('api-port').value) || 18080;
            config.general.this_computer_ip = document.getElementById('this-computer-ip').value;
            config.general.start_on_boot = document.getElementById('start-on-boot').checked;
            config.general.settings_hotkey = document.getElementById('settings-hotkey').value;
            config.general.sleep_hotkey = document.getElementById('sleep-hotkey').value;
            config.general.role = document.getElementById('role').value;
            config.general.coordinator_addr = document.getElementById('coordinator-addr').value;
            config.general.agent_profile = document.getElementById('agent-profile').value;
            config.general.usb_forwarding_enabled = document.getElementById('usb-forwarding-enabled').checked;
            // Only update input_capture_enabled if not using dynamic control
            const agentProfile = document.getElementById('agent-profile').value.trim();
            if (agentProfile === '') {
                config.general.input_capture_enabled = document.getElementById('input-capture-enabled').checked;
            }
            config.general.escape_hotkey = document.getElementById('escape-hotkey').value;
        }

        function renderProfiles() {
            const container = document.getElementById('profiles-list');
            const isAgent = config.general.role === 'agent';
            if (config.profiles.length === 0) {
                container.innerHTML = '<p style="color: #94a3b8;">No profiles configured.</p>';
                return;
            }

            container.innerHTML = config.profiles.map((profile, idx) => ` + "`" + `
                <div class="profile-item" style="${isAgent ? 'opacity: 0.8;' : ''}">
                    <div class="profile-header">
                        <input type="text" class="profile-name" value="${escapeHtml(profile.name)}"
                               ${isAgent ? 'disabled' : ''}
                               onchange="updateProfileName(${idx}, this.value)" 
                               style="background: transparent; border: none; font-size: 1.1rem; font-weight: 600; color: #e2e8f0; width: 200px;">
                        <div class="action-btns">
                            <button class="btn btn-small btn-secondary" onclick="switchToProfile(${jsArg(profile.name)})">Switch</button>
                            ${isAgent ? '' : "<button class=\"btn btn-small btn-danger\" onclick=\"deleteProfile(${idx})\">Delete</button>"}
                        </div>
                    </div>
                    <div class="input-grid" style="display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-bottom: 0.75rem;">
                        <div class="input-group">
                            <label>Hotkey:</label>
                            <div style="display: flex; gap: 0.5rem;">
                                <input type="text" id="hotkey-${idx}" value="${escapeHtml(profile.hotkey || '')}"
                                       ${isAgent ? 'disabled' : ''}
                                       onchange="updateProfileHotkey(${idx}, this.value)"
                                       placeholder="Ctrl+Alt+1" style="flex: 1;">
                                ${isAgent ? '' : "<button class=\"btn btn-small\" style=\"background: #ef4444;\" onclick=\"startRecording(" + idx + ")\">🔴 Record</button>"}
                            </div>
                            ${(profile.hotkey && profile.hotkey.toUpperCase().includes('CTRL')) ? '<div style="font-size: 0.75rem; color: #a5b4fc; margin-top: 0.25rem;">⌘ On macOS: ' + escapeHtml(profile.hotkey.toUpperCase().replace(/CTRL/g, 'Cmd')) + '</div>' : ''}
                        </div>
                        <div class="input-group">
                            <label>Switch Mode:</label>
                            <select onchange="updateProfileSwitchMode(${idx}, this.value)" ${isAgent ? 'disabled' : ''}>
                                <option value="both" ${profile.switch_mode === 'both' || !profile.switch_mode ? 'selected' : ''}>Both (Local & Remote)</option>
                                <option value="local" ${profile.switch_mode === 'local' ? 'selected' : ''}>Local (DDC Only)</option>
                                <option value="remote" ${profile.switch_mode === 'remote' ? 'selected' : ''}>Remote (Notify Only)</option>
                            </select>
                        </div>
                    </div>




                    <div style="margin-top: 1rem; padding-top: 1rem; border-top: 1px solid rgba(255,255,255,0.05);">
                        <div style="font-size: 0.875rem; color: #a5b4fc; margin-bottom: 0.5rem;">Monitor Inputs</div>
                        <div class="monitor-inputs">
                            ${monitors.map(m => ` + "`" + `
                                <div class="input-group">
                                    <label>${escapeHtml((m.name && m.name.length>0) ? (m.name + (m.device_name ? ' ('+m.device_name+')' : '')) : (m.device_name || m.id))}:</label>
                                    <select data-profile-idx="${idx}" data-monitor-id="${escapeHtml(m.id)}" onchange="updateProfileMonitorInput(this)">
                                        <option value="">-</option>
                                        <option value="15" ${(profile.monitor_inputs && profile.monitor_inputs[m.id]==15)?'selected':''}>DP1</option>
                                        <option value="16" ${(profile.monitor_inputs && profile.monitor_inputs[m.id]==16)?'selected':''}>DP2</option>
                                        <option value="17" ${(profile.monitor_inputs && profile.monitor_inputs[m.id]==17)?'selected':''}>HDMI1</option>
                                        <option value="18" ${(profile.monitor_inputs && profile.monitor_inputs[m.id]==18)?'selected':''}>HDMI2</option>
                                        <option value="27" ${(profile.monitor_inputs && profile.monitor_inputs[m.id]==27)?'selected':''}>USB-C</option>
                                    </select>
                                </div>
                            ` + "`" + `).join('')}
                        </div>
                    </div>
                </div>
            ` + "`" + `).join('');
        }

        function renderMonitors() {
            const container = document.getElementById('monitors-info');
            if (monitors.length === 0) {
                container.innerHTML = '<p style="color: #94a3b8;">No external monitors detected.</p>';
                return;
            }
            
            const inputNames = {
                15: 'DP1', 16: 'DP2', 17: 'HDMI1', 18: 'HDMI2', 27: 'USB-C'
            };
            
            container.innerHTML = monitors.map(m => ` + "`" + `
                <div style="padding: 0.75rem; background: rgba(255,255,255,0.03); border-radius: 8px; margin-bottom: 0.5rem;">
                    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem;">
                        <div>
                            <strong>${escapeHtml((m.name && m.name.length>0) ? m.name : (m.device_name || m.id))}</strong> <span style="color: #94a3b8; font-size: 0.875rem;">${escapeHtml((m.name && m.name.length>0 && m.device_name) ? ' ('+m.device_name+')' : ' (ID: '+m.id+')')}</span>
                        </div>
                        <div style="font-size: 0.875rem;">
                            ${m.ddc_supported 
                                ? '<span style="color: #34d399;">✓ DDC Supported</span>' 
                                : '<span style="color: #f87171;">✗ DDC Not Supported</span>'}
                        </div>
                    </div>
                    ${m.input_source ? ` + "`" + `
                        <div style="font-size: 0.875rem; color: #a5b4fc;">
                            Current Input: <strong>${escapeHtml(inputNames[m.input_source] || 'Unknown (0x' + m.input_source.toString(16) + ')')}</strong>
                        </div>
                    ` + "`" + ` : ''}
                </div>
            ` + "`" + `).join('');
        }



        function addProfile() {
            const newProfile = {
                name: 'PC' + (config.profiles.length + 1),
                hotkey: 'Ctrl+Alt+' + (config.profiles.length + 1),
                monitor_inputs: {}
            };
            config.profiles.push(newProfile);
            renderProfiles();
        }

        function deleteProfile(idx) {
            if (confirm('Delete this profile?')) {
                config.profiles.splice(idx, 1);
                renderProfiles();
            }
        }

        function updateProfileName(idx, name) {
            config.profiles[idx].name = name;
        }

        function updateProfileHotkey(idx, hotkey) {
            config.profiles[idx].hotkey = hotkey;
        }

        function updateProfileSwitchMode(idx, mode) {
            config.profiles[idx].switch_mode = mode;
        }



        async function scanNetwork() {
            const container = document.getElementById('discovery-list');
            container.innerHTML = '<p style="color: #94a3b8;">Scanning network... this may take a few seconds.</p>';
            
            try {
                const res = await fetch('/api/discover');
                if (!res.ok) throw new Error('Scan failed');
                const hosts = await res.json();
                
                if (!hosts || hosts.length === 0) {
                    container.innerHTML = '<p style="color: #94a3b8;">No other VKVM instances found on local network.</p>';
                    return;
                }

                container.innerHTML = hosts.map(h => ` + "`" + `
                    <div style="display: flex; justify-content: space-between; align-items: center; padding: 0.75rem; background: rgba(255,255,255,0.03); border-radius: 8px; margin-bottom: 0.5rem;">
                        <div>
                            <strong>${escapeHtml(h.ip)}</strong> <span style="color: #94a3b8;">(Port: ${escapeHtml(h.port)})</span>
                            <div style="font-size: 0.8rem; color: #a5b4fc;">Profile: ${escapeHtml(h.current_profile || 'None')}</div>
                        </div>
                        <div style="display: flex; gap: 0.5rem; align-items: center;">
                            <button class="btn btn-small btn-secondary" onclick="addRemoteFromDiscovery(${jsArg(h.ip + ':' + h.port)})">Add as Remote</button>
                            <button class="btn btn-small" style="background: #4f46e5;" onclick="syncConfigTo(${jsArg(h.ip + ':' + h.port)})">☁️ Sync Config</button>
                        </div>
                    </div>
                ` + "`" + `).join('');
            } catch (e) {
                container.innerHTML = '<p style="color: #f87171;">Scan failed: ' + escapeHtml(e.message) + '</p>';
            }
        }

        async function syncConfigTo(addr) {
            if (!confirm('This will OVERWRITE all settings on ' + addr + ' with your local settings. Continue?')) {
                return;
            }
            
            showStatus('Syncing config to ' + addr + '...');
            try {
                // We pass empty token for now, or might need to ask user if target has token
                const res = await fetch('/api/sync-to?addr=' + encodeURIComponent(addr), {method: 'POST'});
                if (res.ok) {
                    showStatus('Config successfully synced to ' + addr);
                } else {
                    const text = await res.text();
                    showStatus('Sync failed: ' + text, true);
                }
            } catch (e) {
                showStatus('Sync failed: ' + e.message, true);
            }
        }

        function addRemoteFromDiscovery(addr) {
            if (config.profiles.length === 0) {
                showStatus('Add a profile first', true);
                return;
            }
            // Add to all profiles
            let addedCount = 0;
            config.profiles.forEach((p, idx) => {
                if (!p.remote_hosts) p.remote_hosts = [];
                // Avoid duplicates
                if (!p.remote_hosts.find(r => r.address === addr)) {
                    p.remote_hosts.push({address: addr, profile_name: p.name});
                    addedCount++;
                }
            });
            
            if (addedCount > 0) {
                renderProfiles();
                showStatus('Added ' + addr + ' to ' + addedCount + ' profiles.');
            } else {
                showStatus(addr + ' already added to all profiles.');
            }
        }

        function updateProfileMonitorInput(selectEl) {
            const idx = parseInt(selectEl.getAttribute('data-profile-idx'));
            const monitorId = selectEl.getAttribute('data-monitor-id');
            const input = selectEl.value;
            
            if (!config.profiles[idx].monitor_inputs) {
                config.profiles[idx].monitor_inputs = {};
            }
            if (input === '') {
                delete config.profiles[idx].monitor_inputs[monitorId];
            } else {
                config.profiles[idx].monitor_inputs[monitorId] = parseInt(input);
            }
        }

        async function switchToProfile(name) {
            try {
                const res = await fetch('/api/switch?profile=' + encodeURIComponent(name), {method: 'POST'});
                if (!res.ok) throw new Error('Switch failed');
                showStatus('Switched to ' + name);
            } catch (e) {
                showStatus('Switch failed: ' + e.message, true);
            }
        }

        async function sleepDisplay() {
            try {
                const res = await fetch('/api/sleep-display', {method: 'POST'});
                if (!res.ok) throw new Error('Action failed');
                showStatus('Display entering sleep mode...');
            } catch (e) {
                showStatus('Sleep failed: ' + e.message, true);
            }
        }

        async function saveConfig() {
            try {
                // Ensure latest general config (role, etc) is synced before sending
                updateGeneralConfig();
                
                const res = await fetch('/api/config', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify(config)
                });
                if (!res.ok) throw new Error('Save failed');
                const result = await res.json();
                const restartRequired = Array.isArray(result.restart_required) ? result.restart_required : [];
                if (restartRequired.length > 0) {
                    showStatus('Settings saved. Restart VKVM to apply: ' + restartRequired.join(', '));
                } else {
                    showStatus('Settings saved!');
                }
            } catch (e) {
                showStatus('Save failed: ' + e.message, true);
            }
        }

        let recordingIdx = -1;
        let currentHotkey = '';

        function startRecording(idx) {
            recordingIdx = idx;
            currentHotkey = '';
            document.getElementById('recorded-display').textContent = 'Press Keys...';
            document.getElementById('hotkey-recorder').style.display = 'flex';
            window.addEventListener('keydown', captureKeyEvent);
            window.addEventListener('mousedown', captureMouseEvent);
            window.addEventListener('auxclick', captureMouseEvent);
            window.addEventListener('contextmenu', preventContext);
        }

        function cancelRecording() {
            stopRecordingListeners();
            document.getElementById('hotkey-recorder').style.display = 'none';
        }

        function saveRecording() {
            if (currentHotkey) {
                if (recordingIdx === 'settings') {
                    config.general.settings_hotkey = currentHotkey;
                    renderGeneral();
                } else if (recordingIdx === 'sleep') {
                    config.general.sleep_hotkey = currentHotkey;
                    renderGeneral();
                } else if (recordingIdx === 'escape') {
                    config.general.escape_hotkey = currentHotkey;
                    renderGeneral();
                } else if (recordingIdx !== -1) {
                    config.profiles[recordingIdx].hotkey = currentHotkey;
                    renderProfiles();
                }
            }
            cancelRecording();
        }

        function stopRecordingListeners() {
            window.removeEventListener('keydown', captureKeyEvent);
            window.removeEventListener('mousedown', captureMouseEvent);
            window.removeEventListener('auxclick', captureMouseEvent);
            window.removeEventListener('contextmenu', preventContext);
        }

        function preventContext(e) { e.preventDefault(); }

        function captureKeyEvent(e) {
            e.preventDefault();
            e.stopPropagation();

            const keys = [];
            if (e.ctrlKey) keys.push('Ctrl');
            if (e.altKey) keys.push('Alt');
            if (e.shiftKey) keys.push('Shift');
            if (e.metaKey) keys.push('Cmd');

            const key = e.key;
            if (key !== 'Control' && key !== 'Alt' && key !== 'Shift' && key !== 'Meta') {
                let keyLabel = key.toUpperCase();
                if (key === ' ') keyLabel = 'Space';
                keys.push(keyLabel);
                
                currentHotkey = keys.join('+');
                document.getElementById('recorded-display').textContent = currentHotkey;
            } else {
                document.getElementById('recorded-display').textContent = keys.join('+') + (keys.length > 0 ? '+' : '');
            }
        }

        function captureMouseEvent(e) {
            if (e.button === 0) return; // Ignore Left click
            e.preventDefault();
            e.stopPropagation();
            
            const mouseBtn = 'Mouse' + (e.button + 1);
            
            // Generate full hotkey string including modifiers held
            const keys = [];
            if (e.ctrlKey) keys.push('Ctrl');
            if (e.altKey) keys.push('Alt');
            if (e.shiftKey) keys.push('Shift');
            if (e.metaKey) keys.push('Cmd');
            
            // If we are appending complex mouse combinations
            if (currentHotkey && currentHotkey.includes('Mouse') && !currentHotkey.includes(mouseBtn)) {
                currentHotkey += '+' + mouseBtn;
            } else if (!currentHotkey.includes(mouseBtn)) {
                keys.push(mouseBtn);
                currentHotkey = keys.join('+');
            }
            
            document.getElementById('recorded-display').textContent = currentHotkey;
        }

        function showStatus(msg, isError = false) {
            const bar = document.getElementById('status-bar');
            bar.textContent = msg;
            bar.style.display = 'block';
            bar.style.background = isError ? 'rgba(239,68,68,0.9)' : 'rgba(34,197,94,0.9)';
            setTimeout(() => bar.style.display = 'none', 3000);
        }

        loadData();
    </script>
</body>
</html>`))
