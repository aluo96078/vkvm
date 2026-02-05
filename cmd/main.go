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

	// Debug: Log configuration
	log.Printf("[DEBUG] Configuration: Role=%s, APIEnabled=%t", cfg.General.Role, cfg.General.APIEnabled)

	// Input capture for host mode (capture and broadcast to agents)
	var inputTrap *input.Trap
	if cfg.General.Role == "host" && cfg.General.APIEnabled {
		log.Printf("[HOST] Starting input capture for host mode")
		inputTrap = input.NewTrap()

		// Start input capture
		if err := inputTrap.Start(); err != nil {
			log.Printf("[HOST] Failed to start input capture: %v", err)
		} else {
			log.Printf("[HOST] Input capture started successfully")

			// Process captured events and broadcast to agents
			go func() {
				eventCount := 0
				for event := range inputTrap.Events() {
					eventCount++
					log.Printf("[HOST-CAPTURE] Event #%d: %s (dx:%d, dy:%d, btn:%d, pressed:%v, key:0x%X, modifiers:0x%X, ts:%d)",
						eventCount, event.Type, event.DeltaX, event.DeltaY,
						event.Button, event.Pressed, event.KeyCode, event.Modifiers, event.Timestamp)

					// Broadcast to all connected agents
					apiServer.BroadcastInput(
						event.Type,
						event.DeltaX, event.DeltaY,
						event.Button, event.Pressed,
						event.KeyCode, event.Modifiers,
						event.Timestamp,
					)
					log.Printf("[HOST-BROADCAST] Event #%d broadcasted to agents", eventCount)
				}
			}()
		}
	}

	// Input capture for agent mode (receive from host and inject)
	var wsClient *network.WSClient
	if cfg.General.Role == "agent" && cfg.General.CoordinatorAddr != "" {
		log.Printf("[AGENT] Starting agent mode to receive input from host")

		// Create input injector
		injector := input.NewInjector()
		log.Printf("[AGENT] Input injector created")

		// Set up WebSocket client for agent
		wsClient = network.NewWSClient(cfg.General.CoordinatorAddr, cfg.General.APIToken)

		// Set up event handler for received input events
		receivedCount := 0
		wsClient.OnInput = func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, timestamp int64) {
			receivedCount++
			log.Printf("[AGENT-RECEIVE] Event #%d: %s (dx:%d, dy:%d, btn:%d, pressed:%v, key:0x%X, modifiers:0x%X, ts:%d)",
				receivedCount, eventType, deltaX, deltaY, button, pressed, keyCode, modifiers, timestamp)

			// Inject input on macOS agent
			var err error
			switch eventType {
			case "mouse_move":
				err = injector.InjectMouseMove(deltaX, deltaY)
				if err != nil {
					log.Printf("[AGENT-INJECT] Failed to inject mouse move: %v", err)
				} else {
					log.Printf("[AGENT-INJECT] Event #%d: Mouse move injected successfully", receivedCount)
				}
			case "mouse_btn":
				err = injector.InjectMouseButton(button, pressed)
				if err != nil {
					log.Printf("[AGENT-INJECT] Failed to inject mouse button: %v", err)
				} else {
					log.Printf("[AGENT-INJECT] Event #%d: Mouse button %d %s injected successfully", 
						receivedCount, button, map[bool]string{true: "pressed", false: "released"}[pressed])
				}
			case "key":
				err = injector.InjectKey(keyCode, pressed, modifiers)
				if err != nil {
					log.Printf("[AGENT-INJECT] Failed to inject key: %v", err)
				} else {
					log.Printf("[AGENT-INJECT] Event #%d: Key 0x%X %s injected successfully", 
						receivedCount, keyCode, map[bool]string{true: "pressed", false: "released"}[pressed])
				}
			default:
				log.Printf("[AGENT-INJECT] Unknown event type: %s", eventType)
			}
		}

		wsClient.Start()
		log.Printf("[AGENT] Connected to host, ready to receive input events")
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
		wsClient.OnInput = func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, timestamp int64) {
			log.Printf("[AGENT] Received input event: %s (dx:%d, dy:%d, btn:%d, pressed:%v, key:0x%X, modifiers:0x%X)",
				eventType, deltaX, deltaY, button, pressed, keyCode, modifiers)

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
		log.Printf("Event #%d: %s (dx:%d, dy:%d, btn:%d, pressed:%v, key:0x%X)",
			eventCount, event.Type, event.DeltaX, event.DeltaY,
			event.Button, event.Pressed, event.KeyCode)

		// Send to remote if WebSocket is connected
		if wsClient != nil && wsClient.IsConnected() {
			log.Printf("Sending event to remote host")
			wsClient.SendInputEvent(
				event.Type,
				event.DeltaX, event.DeltaY,
				event.Button, event.Pressed,
				event.KeyCode, event.Modifiers,
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
		wsClient.OnInput = func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, timestamp int64) {
			log.Printf("Received input: %s (dx:%d, dy:%d, btn:%d, pressed:%v, key:0x%X)",
				eventType, deltaX, deltaY, button, pressed, keyCode)

			switch eventType {
			case "mouse_move":
				if err := injector.InjectMouseMove(deltaX, deltaY); err != nil {
					log.Printf("Failed to inject mouse move: %v", err)
				}
			case "mouse_btn":
				if err := injector.InjectMouseButton(button, pressed); err != nil {
					log.Printf("Failed to inject mouse button: %v", err)
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
