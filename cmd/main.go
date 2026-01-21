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
	"vkvm/internal/osutils"
	"vkvm/internal/switcher"
	"vkvm/internal/tray"
	"vkvm/internal/ui"
)

var (
	version  = "0.2.0"
	showUI   = flag.Bool("ui", false, "Open the configuration UI")
	listMons = flag.Bool("list", false, "List connected monitors")
	switchTo = flag.String("switch", "", "Switch to profile name")
	showVer  = flag.Bool("version", false, "Show version")
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
	if cfg.General.APIEnabled {
		// New: Ensure firewall rule exists on Windows
		if runtime.GOOS == "windows" {
			go func() {
				if err := osutils.EnsureFirewallRule(cfg.General.APIPort); err != nil {
					log.Printf("Firewall warning: %v", err)
				}
			}()
		}

		apiServer := api.NewServer(cfgMgr, sw)
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
