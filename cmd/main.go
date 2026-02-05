// VKVM - Virtual KVM Switcher
// A cross-platform monitor input switcher using DDC/CI
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"vkvm/internal/api"
	"vkvm/internal/config"
	"vkvm/internal/hotkey"
	"vkvm/internal/input"
	"vkvm/internal/network"
	"vkvm/internal/osutils"
	"vkvm/internal/switcher"
	"vkvm/internal/tray"
	"vkvm/internal/ui"
)

var (
	version   = "0.2.0"
	showUI    = flag.Bool("ui", false, "Open the configuration UI")
	listMons  = flag.Bool("list", false, "List connected monitors")
	switchTo  = flag.String("switch", "", "Switch to profile name")
	showVer   = flag.Bool("version", false, "Show version")
	testInput = flag.Bool("test-input", false, "Test input capture and forwarding")
)

func main() {
	flag.Parse()

	if *showVer {
		fmt.Printf("vkvm version %s\n", version)
		return
	}

	// Initialize config
	cfgMgr, err := config.NewManager()
	if err != nil {
		log.Fatalf("Failed to initialize config: %v", err)
	}
	if err := cfgMgr.Load(); err != nil {
		log.Printf("Warning: failed to load config: %v", err)
	}

	// Handle --list flag
	if *listMons {
		listMonitors(cfgMgr)
		return
	}

	// Handle --switch flag
	if *switchTo != "" {
		handleSwitch(cfgMgr, *switchTo)
		return
	}

	// Handle --ui flag
	if *showUI {
		runUI(cfgMgr)
		return
	}

	// Handle --test-input flag
	if *testInput {
		runInputTest(cfgMgr)
		return
	}

	// Default: run as background service
	runService(cfgMgr)
}

func listMonitors(cfgMgr *config.Manager) {
	sw, err := switcher.New(cfgMgr)
	if err != nil {
		log.Fatalf("Failed to create switcher: %v", err)
	}

	monitors, err := sw.ListMonitors()
	if err != nil {
		log.Fatalf("Failed to list monitors: %v", err)
	}

	fmt.Println("Connected Monitors:")
	fmt.Println("-------------------")
	for _, mon := range monitors {
		fmt.Printf("ID: %s\n", mon.ID)
		fmt.Printf("  Name: %s\n", mon.Name)
		if mon.Serial != "" {
			fmt.Printf("  Serial: %s\n", mon.Serial)
		}
		if mon.DDCSupported {
			fmt.Printf("  DDC/CI: ✓ Supported\n")
		} else {
			fmt.Printf("  DDC/CI: ✗ Not supported\n")
		}
		fmt.Println()
	}
}

func handleSwitch(cfgMgr *config.Manager, profileName string) {
	sw, err := switcher.New(cfgMgr)
	if err != nil {
		log.Fatalf("Failed to create switcher: %v", err)
	}

	if err := sw.SwitchToProfile(profileName); err != nil {
		log.Fatalf("Failed to switch to profile %s: %v", profileName, err)
	}
	fmt.Printf("Switched to profile: %s\n", profileName)
}

func runUI(cfgMgr *config.Manager) {
	// Create switcher for the UI
	sw, err := switcher.New(cfgMgr)
	if err != nil {
		log.Printf("Failed to create switcher: %v", err)
		return
	}

	// Start the UI server
	server := ui.NewServer(cfgMgr, sw)
	log.Println("Starting configuration UI...")

	// Check if running from CLI (blocking mode) or from tray (non-blocking)
	// When called from main with --ui flag, we should block
	if *showUI {
		// Blocking mode for CLI
		if err := server.Start(); err != nil {
			log.Printf("UI server error: %v", err)
		}
	} else {
		// Non-blocking mode for tray
		go func() {
			if err := server.Start(); err != nil {
				log.Printf("UI server error: %v", err)
			}
		}()
	}
}

func runService(cfgMgr *config.Manager) {
	log.Println("VKVM Service starting...")

	// Create switcher
	sw, err := switcher.New(cfgMgr)
	if err != nil {
		log.Fatalf("Failed to create switcher: %v", err)
	}

	// WebSocket client for agent mode
	var wsClient *network.WSClient

	// Input trap for host mode
	var inputTrap *input.Trap

	// Start API server if enabled
	cfg := cfgMgr.Get()
	var apiServer *api.Server
	if cfg.General.APIEnabled {
		// New: Ensure firewall rule exists on Windows
		if runtime.GOOS == "windows" {
			go func() {
				if err := osutils.EnsureFirewallRule(cfg.General.APIPort); err != nil {
					log.Printf("Firewall warning: %v", err)
				}
			}()
		}

		apiServer = api.NewServer(cfgMgr, sw)

		// Wire up switcher -> api broadcast for WebSocket
		sw.SetOnSwitch(func(profileName string) {
			// Broadcast the switch event to all connected agents
			// Origin is "host" because this callback is triggered by a local decision/action on the host
			// (or a successfully processed agent request)
			apiServer.BroadcastSwitch(profileName, "host")

			// Local logic for host: control input capture based on active profile
			if cfg.General.Role == "host" && inputTrap != nil && cfg.General.AgentProfile != "" {
				allowCapture := (profileName == cfg.General.AgentProfile)
				log.Printf("Switch to profile '%s', agent profile '%s', allow capture: %v", profileName, cfg.General.AgentProfile, allowCapture)
				inputTrap.EnableCapture(allowCapture)
			}
		})

		go func() {
			if err := apiServer.Start(cfg.General.APIPort); err != nil {
				log.Printf("API server error: %v", err)
			}
		}()
	}

	// Hotkey manager
	hkMgr := hotkey.NewManager()
	if err := hkMgr.Start(); err != nil {
		log.Printf("Warning: Hotkey Engine failed to start: %v", err)
	}

	// Input handling based on role
	log.Printf("Role: %s, CoordinatorAddr: %s", cfg.General.Role, cfg.General.CoordinatorAddr)
	if cfg.General.Role == "agent" && cfg.General.CoordinatorAddr != "" {
		// Create input injector
		injector := input.NewInjector()

		// Agent input injection control
		var (
			allowInjection bool
			injectionMutex sync.Mutex
		)

		// Periodic detection of current display state
		if cfg.General.AgentProfile != "" {
			// Start detection goroutine
			go func() {
				ticker := time.NewTicker(1 * time.Second) // Check every second
				defer ticker.Stop()

				for range ticker.C {
					detectedProfile, err := sw.DetectActiveProfile()
					if err != nil {
						continue
					}

					injectionMutex.Lock()
					oldAllow := allowInjection
					allowInjection = (detectedProfile == cfg.General.AgentProfile)
					injectionMutex.Unlock()

					if oldAllow != allowInjection {
						log.Printf("Agent: Periodic check - detected profile '%s', agent profile '%s', allow injection: %v", detectedProfile, cfg.General.AgentProfile, allowInjection)
					}
				}
			}()
		} else {
			// Auto-detect disabled, always allow injection
			allowInjection = true
		}

		// Set up WebSocket client for agent
		wsClient = network.NewWSClient(cfg.General.CoordinatorAddr, cfg.General.APIToken)

		// Set up event handler for received input events
		wsClient.OnInput = func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
			// Check if USB forwarding is enabled from Host config
			currentCfg := cfgMgr.Get()
			if !currentCfg.General.USBForwardingEnabled {
				// USB forwarding disabled by Host, ignore input
				return
			}

			// Check if injection is allowed based on profile
			injectionMutex.Lock()
			shouldInject := allowInjection
			injectionMutex.Unlock()

			if !shouldInject {
				// Silently ignore input when not displaying this agent
				return
			}

			// Inject input on Agent
			switch eventType {
			case "mouse_move":
				injector.InjectMouseMove(deltaX, deltaY)
			case "mouse_btn":
				injector.InjectMouseButton(button, pressed)
			case "mouse_wheel":
				injector.InjectMouseWheel(wheelDelta, 0)
			case "mouse_wheel_h":
				injector.InjectMouseWheel(0, wheelDelta)
			case "key":
				injector.InjectKey(keyCode, pressed, modifiers)
			}
		}

		// Set up switch event handler to control injection based on active profile
		wsClient.OnSwitch = func(profile string) {
			injectionMutex.Lock()
			defer injectionMutex.Unlock()
			if cfg.General.AgentProfile != "" {
				allowInjection = (profile == cfg.General.AgentProfile)
				log.Printf("Agent: Switch event received for profile '%s', agent profile '%s', allow injection: %v", profile, cfg.General.AgentProfile, allowInjection)
			} else {
				// If no specific agent profile configured, always allow injection
				allowInjection = true
				log.Printf("Agent: No agent profile configured, allowing injection")
			}
		}

		wsClient.Start()
	} else if cfg.General.Role == "host" {
		// Check administrator privileges on Windows
		if runtime.GOOS == "windows" {
			log.Println("Note: Input capture requires administrator privileges")
			log.Println("Please ensure you're running this application as Administrator")
		}

		// Start input capture on host (only if USB forwarding is enabled)
		log.Printf("Host mode: USB Forwarding Enabled: %v", cfg.General.USBForwardingEnabled)
		if cfg.General.USBForwardingEnabled {
			inputTrap = input.NewTrap()

			log.Printf("Host mode: AgentProfile='%s'", cfg.General.AgentProfile)

			// Initial capture state: check current profile
			if cfg.General.AgentProfile != "" {
				detectedProfile, err := sw.DetectActiveProfile()
				if err == nil && detectedProfile == cfg.General.AgentProfile {
					log.Printf("Initial profile '%s' matches agent profile, enabling capture", detectedProfile)
					inputTrap.EnableCapture(true)
				} else {
					log.Printf("Initial profile '%s' does not match agent profile '%s', capture disabled", detectedProfile, cfg.General.AgentProfile)
				}
			} else if cfg.General.InputCaptureEnabled {
				// Fallback to config if no agent profile set
				log.Printf("No agent profile set, using config InputCaptureEnabled: %v", cfg.General.InputCaptureEnabled)
				inputTrap.EnableCapture(true)
			} else {
				log.Printf("Input capture not enabled")
			}

			if err := inputTrap.Start(); err == nil {
				// Process captured events and broadcast to all connected agents
				go func() {
					for event := range inputTrap.Events() {
						// Broadcast input event to all connected agents via API server
						if apiServer != nil {
							apiServer.BroadcastInput(
								event.Type,
								event.DeltaX, event.DeltaY,
								event.Button, event.Pressed,
								event.KeyCode, event.Modifiers,
								event.WheelDelta,
								event.Timestamp,
							)
						}
					}
				}()
			}
		} else {
			log.Printf("USB forwarding disabled, skipping input capture")
		}
	}

	// Tray instance
	t := tray.New("VKVM - KVM Switcher")

	// Debouncer for hotkeys
	var lastHkTime time.Time
	var hkMux sync.Mutex
	debounce := func() bool {
		hkMux.Lock()
		defer hkMux.Unlock()
		if time.Since(lastHkTime) < 500*time.Millisecond {
			return false
		}
		lastHkTime = time.Now()
		return true
	}

	// Helper to refresh hotkeys and tray menu on config change
	refreshShortcuts := func() {
		cfg := cfgMgr.Get()
		hkMgr.Clear() // Clear existing registered callbacks

		// Register emergency escape hotkey for Host with input capture
		if cfg.General.Role == "host" && cfg.General.EscapeHotkey != "" {
			_, err := hkMgr.Register(cfg.General.EscapeHotkey, func() {
				log.Printf("EMERGENCY: Escape hotkey pressed - disabling input capture")
				if inputTrap != nil {
					inputTrap.EnableCapture(false)
				}
				// Also update config to persist the disabled state
				cfg := cfgMgr.Get()
				cfg.General.InputCaptureEnabled = false
				cfgMgr.Set(cfg)
				if err := cfgMgr.Save(); err != nil {
					log.Printf("Failed to save config: %v", err)
				}
				log.Printf("Input capture disabled. Use settings to re-enable.")
			})
			if err != nil {
				log.Printf("Warning: failed to register escape hotkey: %v", err)
			} else {
				log.Printf("Registered emergency escape hotkey: %s", cfg.General.EscapeHotkey)
			}
		}

		// Register global settings hotkey
		if cfg.General.SettingsHotkey != "" {
			_, err := hkMgr.Register(cfg.General.SettingsHotkey, func() {
				if !debounce() {
					return
				}
				log.Printf("Hotkey: Opening Settings UI...")
				go runUI(cfgMgr)
			})
			if err != nil {
				log.Printf("Warning: failed to register settings hotkey: %v", err)
			}

			// Cross-platform mapping for settings hotkey
			if runtime.GOOS == "darwin" && strings.Contains(strings.ToUpper(cfg.General.SettingsHotkey), "CTRL") {
				cmdVariant := strings.ReplaceAll(strings.ToUpper(cfg.General.SettingsHotkey), "CTRL", "CMD")
				hkMgr.Register(cmdVariant, func() {
					if !debounce() {
						return
					}
					log.Printf("Hotkey: Opening Settings UI...")
					go runUI(cfgMgr)
				})
			}
		}

		// Register global sleep hotkey
		if cfg.General.SleepHotkey != "" {
			_, err := hkMgr.Register(cfg.General.SleepHotkey, func() {
				if !debounce() {
					return
				}
				log.Printf("Hotkey: Sleeping Displays...")
				// Execute sleep in a separate goroutine so it doesn't block the hotkey thread
				go func() {
					// Wait a bit to prevent immediate wake from key release
					time.Sleep(500 * time.Millisecond)
					if err := osutils.TurnOffDisplay(); err != nil {
						log.Printf("Error sleeping displays: %v", err)
					}
				}()
			})
			if err != nil {
				log.Printf("Warning: failed to register sleep hotkey: %v", err)
			}

			// Cross-platform mapping for sleep hotkey
			if runtime.GOOS == "darwin" && strings.Contains(strings.ToUpper(cfg.General.SleepHotkey), "CTRL") {
				cmdVariant := strings.ReplaceAll(strings.ToUpper(cfg.General.SleepHotkey), "CTRL", "CMD")
				hkMgr.Register(cmdVariant, func() {
					if !debounce() {
						return
					}
					log.Printf("Hotkey: Sleeping Displays...")
					go func() {
						// Wait a bit to prevent immediate wake from key release
						time.Sleep(500 * time.Millisecond)
						if err := osutils.TurnOffDisplay(); err != nil {
							log.Printf("Error sleeping displays: %v", err)
						}
					}()
				})
			}
		}

		for _, profile := range cfg.Profiles {
			if profile.Hotkey == "" {
				continue
			}
			pName := profile.Name
			hotkey := profile.Hotkey

			// Register the original hotkey
			_, err := hkMgr.Register(hotkey, func() {
				if !debounce() {
					return
				}
				log.Printf("Hotkey: Switching to %s...", pName)
				if err := sw.SwitchToProfile(pName); err != nil {
					log.Printf("Switch error: %v", err)
				}
			})
			if err != nil {
				log.Printf("Warning: failed to register hotkey for profile %s: %v", pName, err)
			}

			// Cross-platform mapping: on macOS, also register CMD variant if CTRL is present
			if runtime.GOOS == "darwin" && strings.Contains(strings.ToUpper(hotkey), "CTRL") {
				cmdVariant := strings.ReplaceAll(strings.ToUpper(hotkey), "CTRL", "CMD")
				_, _ = hkMgr.Register(cmdVariant, func() {
					if !debounce() {
						return
					}
					log.Printf("Hotkey: Switching to %s...", pName)
					if err := sw.SwitchToProfile(pName); err != nil {
						log.Printf("Switch error: %v", err)
					}
				})
			}
		}
		log.Printf("Shortcuts: Refreshed %d profiles", len(cfg.Profiles))
	}

	// Initial shortcut setup
	refreshShortcuts()

	// Register callback to refresh shortcuts when config changes (e.g. via API)
	cfgMgr.RegisterChangeCallback(refreshShortcuts)

	// Agent sync loop: Periodic sync from Host
	if cfg.General.Role == "agent" && cfg.General.CoordinatorAddr != "" {
		log.Printf("Service: Initial sync from Host %s...", cfg.General.CoordinatorAddr)
		// One immediate sync on startup (synchronous)
		if err := sw.SyncProfiles(); err == nil {
			refreshShortcuts()
		} else {
			log.Printf("Warning: Initial sync from Host failed: %v", err)
		}

		go func() {
			// Periodic sync every 2 minutes
			ticker := time.NewTicker(2 * time.Minute)
			for range ticker.C {
				if err := sw.SyncProfiles(); err == nil {
					refreshShortcuts()
				}
			}
		}()
	}

	// Add menu items for each profile (Note: Tray menu currently only supports initial setup)
	for _, profile := range cfg.Profiles {
		profileName := profile.Name // Capture for closure
		t.AddMenuItem(fmt.Sprintf("Switch to %s", profileName), func() {
			if err := sw.SwitchToProfile(profileName); err != nil {
				log.Printf("Switch error: %v", err)
			}
		})
	}

	t.AddSeparator()

	t.AddMenuItem("Settings...", func() {
		go runUI(cfgMgr)
	})

	t.AddSeparator()

	t.AddMenuItem("Quit", func() {
		t.Stop()
	})

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		t.Stop()
	}()

	log.Println("VKVM Service running. Press Ctrl+C to stop.")
	t.Run()
}

func runInputTest(cfgMgr *config.Manager) {
	log.Println("Starting input forwarding test...")

	switch runtime.GOOS {
	case "windows":
		runWindowsInputTest(cfgMgr)
	case "darwin":
		runMacInputTest(cfgMgr)
	default:
		log.Fatalf("Input test not supported on %s", runtime.GOOS)
	}
}

func runWindowsInputTest(cfgMgr *config.Manager) {
	log.Println("Running Windows input capture test")

	// Check if running as administrator
	if runtime.GOOS == "windows" {
		// Simple check for administrator privileges on Windows
		// This is a basic check - in production you might want more robust checking
		log.Println("Note: Raw Input capture requires administrator privileges")
		log.Println("Please ensure you're running this application as Administrator")
	}

	// Create input trap
	trap := input.NewTrap()

	// Set up kill switch callback
	trap.SetKillSwitch(func() {
		log.Println("Kill switch activated - stopping input capture")
		trap.Stop()
	})

	// Create WebSocket client
	cfg := cfgMgr.Get()
	if cfg.General.Role != "agent" || cfg.General.CoordinatorAddr == "" {
		log.Println("Warning: Not configured as agent or no coordinator address")
		log.Println("Please configure as agent and set coordinator address for full test")
	}

	var wsClient *network.WSClient
	if cfg.General.CoordinatorAddr != "" {
		log.Printf("Connecting to coordinator: %s", cfg.General.CoordinatorAddr)
		wsClient = network.NewWSClient(cfg.General.CoordinatorAddr, cfg.General.APIToken)

		// Set up event handler for received input events
		wsClient.OnInput = func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
			// TODO: Inject input on Windows agent
			// For now, just log the received events
		}

		wsClient.Start()
		defer wsClient.Close()
	}

	// Start capturing input
	log.Println("Starting input capture... Press Ctrl+Alt+Esc to stop")
	if err := trap.Start(); err != nil {
		log.Fatalf("Failed to start input capture: %v", err)
	}
	log.Println("Input capture started successfully")

	// Safety mechanism: auto-stop after 3 minutes
	go func() {
		time.Sleep(3 * time.Minute)
		log.Println("Safety timeout reached - automatically stopping input capture")
		trap.Stop()
	}()

	// Process events
	eventCount := 0
	log.Println("Waiting for input events...")
	for event := range trap.Events() {
		eventCount++
		log.Printf("Event #%d: %s (dx:%d, dy:%d, btn:%d, pressed:%v, key:0x%X, wheel:%d)",
			eventCount, event.Type, event.DeltaX, event.DeltaY,
			event.Button, event.Pressed, event.KeyCode, event.WheelDelta)

		// Send to remote if WebSocket is connected
		if wsClient != nil && wsClient.IsConnected() {
			log.Printf("Sending event to remote host")
			wsClient.SendInputEvent(
				event.Type,
				event.DeltaX, event.DeltaY,
				event.Button, event.Pressed,
				event.KeyCode, event.Modifiers,
				event.WheelDelta,
				event.Timestamp,
			)
		} else if wsClient != nil {
			log.Printf("WebSocket not connected, event not sent")
		}
	}

	log.Printf("Input test completed. Processed %d events", eventCount)
}

func runMacInputTest(cfgMgr *config.Manager) {
	log.Println("Running macOS input injection test")

	// Create input injector
	injector := input.NewInjector()

	// Create WebSocket client for receiving events
	cfg := cfgMgr.Get()
	var wsClient *network.WSClient
	if cfg.General.CoordinatorAddr != "" {
		wsClient = network.NewWSClient(cfg.General.CoordinatorAddr, cfg.General.APIToken)

		// Set up event handler
		wsClient.OnInput = func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
			log.Printf("Received input: %s (dx:%d, dy:%d, btn:%d, pressed:%v, key:0x%X, wheel:%d)",
				eventType, deltaX, deltaY, button, pressed, keyCode, wheelDelta)

			switch eventType {
			case "mouse_move":
				if err := injector.InjectMouseMove(deltaX, deltaY); err != nil {
					log.Printf("Failed to inject mouse move: %v", err)
				}
			case "mouse_btn":
				if err := injector.InjectMouseButton(button, pressed); err != nil {
					log.Printf("Failed to inject mouse button: %v", err)
				}
			case "mouse_wheel":
				if err := injector.InjectMouseWheel(wheelDelta, 0); err != nil {
					log.Printf("Failed to inject mouse wheel: %v", err)
				}
			case "mouse_wheel_h":
				if err := injector.InjectMouseWheel(0, wheelDelta); err != nil {
					log.Printf("Failed to inject horizontal mouse wheel: %v", err)
				}
			case "key":
				if err := injector.InjectKey(keyCode, pressed, modifiers); err != nil {
					log.Printf("Failed to inject key: %v", err)
				}
			}
		}

		wsClient.Start()
		defer wsClient.Close()
	}

	// Wait for events
	log.Println("Waiting for input events... Press Ctrl+C to stop")

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Input injection test completed")
}
