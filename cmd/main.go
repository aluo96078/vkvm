// VKVM - Virtual KVM Switcher
// A cross-platform monitor input switcher using DDC/CI
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"vkvm/internal/api"
	"vkvm/internal/autostart"
	"vkvm/internal/config"
	"vkvm/internal/hotkey"
	"vkvm/internal/input"
	"vkvm/internal/network"
	"vkvm/internal/osutils"
	"vkvm/internal/protocol"
	"vkvm/internal/service"
	"vkvm/internal/switcher"
	"vkvm/internal/tray"
	"vkvm/internal/ui"
)

var (
	version       = "0.2.0"
	showUI        = flag.Bool("ui", false, "Open the configuration UI")
	listMons      = flag.Bool("list", false, "List connected monitors")
	switchTo      = flag.String("switch", "", "Switch to profile name")
	showVer       = flag.Bool("version", false, "Show version")
	testInput     = flag.Bool("test-input", false, "Test input capture and forwarding")
	serviceMode   = flag.Bool("service", false, "Run the non-interactive core under the platform service manager")
	companionMode = flag.Bool("companion", false, "Run only the logged-in user companion and connect to the core over local IPC")
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
	if *serviceMode {
		runPlatformService(cfgMgr)
		return
	}
	if *companionMode {
		runCompanion(cfgMgr)
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

// runCompanion owns only the logged-in user's UI, hotkeys and input hook. The
// authenticated network core remains in the platform service; all control
// and captured input crosses the owner-scoped IPC endpoint. Keeping this
// process small avoids starting a second API/UDP listener after login.
func runCompanion(cfgMgr *config.Manager) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var client *service.IPCClient
	var err error
	for attempt := 0; ctx.Err() == nil; attempt++ {
		client, err = service.DialCompanionIPC(ctx)
		if err == nil {
			break
		}
		if attempt == 0 {
			log.Printf("Companion: core IPC unavailable, waiting for service: %v", err)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		}
	}
	if client == nil {
		if ctx.Err() == nil {
			log.Printf("Companion: core IPC did not become available")
		}
		return
	}
	defer client.Close()
	var callMu sync.Mutex
	call := func(request service.IPCEnvelope) (service.IPCEnvelope, error) {
		callMu.Lock()
		defer callMu.Unlock()
		response, callErr := client.Call(ctx, request)
		if callErr != nil {
			return service.IPCEnvelope{}, callErr
		}
		if response.Type == "error" {
			return service.IPCEnvelope{}, fmt.Errorf("core IPC rejected %s", request.Type)
		}
		return response, nil
	}
	if _, err := call(service.IPCEnvelope{Type: "version", RequestID: "companion-version"}); err != nil {
		log.Printf("Companion: core version negotiation failed: %v", err)
		return
	}
	if _, err := call(service.IPCEnvelope{Type: "status", RequestID: "companion-status"}); err != nil {
		log.Printf("Companion: core status request failed: %v", err)
		return
	}

	cfg := cfgMgr.Get()
	var trap *input.Trap
	var injector *input.Injector
	var inputQueue *input.InjectionQueue
	activeProfile := cfg.General.CurrentProfile
	var activeProfileMu sync.RWMutex
	if cfg.General.Role == "agent" {
		injector = input.NewInjector()
		inputQueue = input.NewInjectionQueue(1024, func(payload protocol.InputPayload) {
			activeProfileMu.RLock()
			profile := activeProfile
			activeProfileMu.RUnlock()
			if cfg.General.AgentProfile == "" || profile != cfg.General.AgentProfile || !input.CurrentPermissions().CanInject() || !cfg.General.USBForwardingEnabled {
				return
			}
			switch payload.Type {
			case "mouse_move":
				_ = injector.InjectMouseMove(payload.DeltaX, payload.DeltaY)
			case "mouse_btn":
				_ = injector.InjectMouseButton(payload.Button, payload.Pressed)
			case "mouse_wheel":
				_ = injector.InjectMouseWheel(payload.WheelDelta, 0)
			case "mouse_wheel_h":
				_ = injector.InjectMouseWheel(0, payload.WheelDelta)
			case "key":
				_ = injector.InjectKeyEvent(payload.KeyCode, payload.ScanCode, payload.Extended, payload.E1, payload.Pressed, payload.Modifiers)
			}
		})
	}
	processCompanionEvents := func(events []service.IPCEnvelope) {
		for _, event := range events {
			switch event.Type {
			case "input":
				var payload protocol.InputPayload
				if json.Unmarshal(event.Payload, &payload) == nil && inputQueue != nil {
					_ = inputQueue.Submit(payload)
				}
			case "input_state":
				var snapshot protocol.InputStatePayload
				if json.Unmarshal(event.Payload, &snapshot) == nil && injector != nil {
					inputQueue.Flush()
					_ = injector.ReleaseAll()
					for _, key := range snapshot.Keys {
						_ = injector.InjectKeyEvent(key.KeyCode, key.ScanCode, key.Extended, key.E1, true, snapshot.Modifiers)
					}
					for _, button := range snapshot.Buttons {
						_ = injector.InjectMouseButton(button, true)
					}
				}
			case "release_all":
				if inputQueue != nil {
					inputQueue.Flush()
				}
				if injector != nil {
					_ = injector.ReleaseAll()
				}
			case "switch":
				var payload struct {
					Profile string `json:"profile"`
				}
				if json.Unmarshal(event.Payload, &payload) == nil {
					activeProfileMu.Lock()
					activeProfile = payload.Profile
					activeProfileMu.Unlock()
					if trap != nil && cfg.General.AgentProfile != "" {
						trap.EnableCapture(payload.Profile == cfg.General.AgentProfile && input.CurrentPermissions().CanCapture())
					}
				}
			}
		}
	}
	if inputQueue != nil {
		defer inputQueue.Close()
	}
	if injector != nil {
		defer func() { _ = injector.ReleaseAll() }()
	}
	var sequence atomic.Uint32
	companionSession := network.NewSessionID()
	if cfg.General.Role == "host" && cfg.General.USBForwardingEnabled && input.CurrentPermissions().CanCapture() {
		trap = input.NewTrap()
		trap.EnableCapture(cfg.General.AgentProfile == "" && cfg.General.InputCaptureEnabled)
		if err := trap.Start(); err != nil {
			log.Printf("Companion: input capture unavailable: %v", err)
			trap = nil
		} else {
			captured := trap
			go func() {
				for event := range captured.Events() {
					payload := companionInputPayload(event, cfg.General.DeviceID, companionSession, sequence.Add(1))
					data, marshalErr := json.Marshal(payload)
					if marshalErr != nil {
						continue
					}
					if _, callErr := call(service.IPCEnvelope{Type: "input", RequestID: fmt.Sprintf("input-%d", payload.Sequence), Payload: data}); callErr != nil {
						log.Printf("Companion: input IPC failed: %v", callErr)
						return
					}
				}
			}()
		}
	}
	defer func() {
		if trap != nil {
			trap.EnableCapture(false)
			_ = trap.Stop()
		}
		_, _ = call(service.IPCEnvelope{Type: "release_all", RequestID: "companion-release"})
	}()

	hkMgr := hotkey.NewManager()
	if err := hkMgr.Start(); err != nil {
		log.Printf("Companion: hotkey engine failed to start: %v", err)
	}
	var lastHkTime time.Time
	var hkMu sync.Mutex
	debounce := func() bool {
		hkMu.Lock()
		defer hkMu.Unlock()
		if time.Since(lastHkTime) < 500*time.Millisecond {
			return false
		}
		lastHkTime = time.Now()
		return true
	}
	refreshShortcuts := func() {
		current := cfgMgr.Get()
		registerCommonShortcuts(
			hkMgr,
			current,
			debounce,
			func() { go runUI(cfgMgr) },
			func() {
				go func() {
					time.Sleep(500 * time.Millisecond)
					if err := osutils.TurnOffDisplay(); err != nil {
						log.Printf("Companion: error sleeping displays: %v", err)
					}
				}()
			},
			func() {
				if trap != nil {
					trap.EnableCapture(false)
				}
				if _, err := call(service.IPCEnvelope{Type: "release_all", RequestID: "companion-hotkey-release"}); err != nil {
					log.Printf("Companion: emergency release failed: %v", err)
				}
			},
			func(profileName string) {
				data, err := json.Marshal(map[string]string{"profile": profileName})
				if err != nil {
					return
				}
				if _, err := call(service.IPCEnvelope{Type: "switch", RequestID: "companion-hotkey-switch", Payload: data}); err != nil {
					log.Printf("Companion: hotkey switch %q failed: %v", profileName, err)
				}
			},
		)
	}
	refreshShortcuts()
	cfgMgr.RegisterChangeCallback(refreshShortcuts)

	trayApp := tray.New("VKVM Companion")
	for _, profile := range cfg.Profiles {
		profileName := profile.Name
		trayApp.AddMenuItem(fmt.Sprintf("Switch to %s", profileName), func() {
			data, marshalErr := json.Marshal(map[string]string{"profile": profileName})
			if marshalErr != nil {
				return
			}
			if _, callErr := call(service.IPCEnvelope{Type: "switch", RequestID: "companion-switch", Payload: data}); callErr != nil {
				log.Printf("Companion: switch %q failed: %v", profileName, callErr)
			}
		})
	}
	trayApp.AddSeparator()
	trayApp.AddMenuItem("Settings...", func() { go runUI(cfgMgr) })
	trayApp.AddSeparator()
	trayApp.AddMenuItem("Quit", func() { cancel(); trayApp.Stop() })
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				response, err := call(service.IPCEnvelope{Type: "poll_input", RequestID: "companion-input-poll"})
				if err != nil {
					log.Printf("Companion: core status subscription lost: %v", err)
					return
				}
				var events []service.IPCEnvelope
				if json.Unmarshal(response.Payload, &events) == nil {
					processCompanionEvents(events)
				}
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := call(service.IPCEnvelope{Type: "subscribe", RequestID: "companion-subscribe"}); err != nil {
					log.Printf("Companion: core status subscription lost: %v", err)
					return
				}
			}
		}
	}()
	trayApp.Run()
}

func companionInputPayload(event input.InputEvent, deviceID, sessionID string, sequence uint32) protocol.InputPayload {
	return protocol.InputPayload{
		Type: event.Type, DeltaX: event.DeltaX, DeltaY: event.DeltaY, Button: event.Button,
		Pressed: event.Pressed, KeyCode: event.KeyCode, Modifiers: event.Modifiers,
		WheelDelta: event.WheelDelta, Timestamp: event.Timestamp, DeviceID: deviceID,
		HardwareID: event.HardwareID, ScanCode: event.ScanCode, Extended: event.Extended,
		E1: event.E1, Repeat: event.Repeat, Sequence: sequence, SessionID: sessionID,
		ClockNS: event.ClockNS,
	}
}

// registerCommonShortcuts installs the settings, sleep, emergency and profile
// actions that are shared by the full interactive process and the split
// companion. Platform-specific event capture is started separately by the
// caller; this function only translates configured names into callbacks.
func registerCommonShortcuts(
	hkMgr *hotkey.Manager,
	cfg *config.Config,
	debounce func() bool,
	openSettings func(),
	sleepDisplays func(),
	emergency func(),
	switchProfile func(string),
) {
	if hkMgr == nil || cfg == nil {
		return
	}
	hkMgr.Clear()
	register := func(name string, callback func()) {
		if strings.TrimSpace(name) == "" || callback == nil {
			return
		}
		if _, err := hkMgr.Register(name, callback); err != nil {
			log.Printf("Warning: failed to register hotkey %q: %v", name, err)
		}
	}
	withDebounce := func(callback func()) func() {
		return func() {
			if debounce == nil || debounce() {
				callback()
			}
		}
	}
	if cfg.General.Role == "host" && emergency != nil {
		register(cfg.General.EscapeHotkey, emergency)
	}
	if openSettings != nil {
		settings := withDebounce(openSettings)
		register(cfg.General.SettingsHotkey, settings)
		if runtime.GOOS == "darwin" && strings.Contains(strings.ToUpper(cfg.General.SettingsHotkey), "CTRL") {
			register(strings.ReplaceAll(strings.ToUpper(cfg.General.SettingsHotkey), "CTRL", "CMD"), settings)
		}
	}
	if sleepDisplays != nil {
		sleep := withDebounce(sleepDisplays)
		register(cfg.General.SleepHotkey, sleep)
		if runtime.GOOS == "darwin" && strings.Contains(strings.ToUpper(cfg.General.SleepHotkey), "CTRL") {
			register(strings.ReplaceAll(strings.ToUpper(cfg.General.SleepHotkey), "CTRL", "CMD"), sleep)
		}
	}
	if switchProfile != nil {
		for _, profile := range cfg.Profiles {
			profileName := profile.Name
			if strings.TrimSpace(profile.Hotkey) == "" || profileName == "" {
				continue
			}
			switchCallback := withDebounce(func() { switchProfile(profileName) })
			register(profile.Hotkey, switchCallback)
			if runtime.GOOS == "darwin" && strings.Contains(strings.ToUpper(profile.Hotkey), "CTRL") {
				register(strings.ReplaceAll(strings.ToUpper(profile.Hotkey), "CTRL", "CMD"), switchCallback)
			}
		}
	}
	log.Printf("Shortcuts: Refreshed %d profiles", len(cfg.Profiles))
}

func runService(cfgMgr *config.Manager) {
	// AppKit requires NSApplication/status-item initialization on the process
	// main thread. Supervisor.Run intentionally executes workers in a goroutine,
	// which makes the macOS tray abort inside systray.registerSystray. LaunchAgent
	// KeepAlive provides crash recovery for this interactive path; the core
	// Supervisor remains in use by non-macOS service modes.
	if runtime.GOOS == "darwin" {
		if err := runServiceContext(cfgMgr, context.Background()); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("VKVM Service stopped: %v", err)
		}
		return
	}
	supervisor := service.NewSupervisor(
		func(ctx context.Context) error { return runServiceContext(cfgMgr, ctx) },
		nil,
		service.SupervisorConfig{MinBackoff: 500 * time.Millisecond, MaxBackoff: 30 * time.Second, MaxRestarts: 5},
	)
	if err := supervisor.Run(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("VKVM Service supervisor stopped: %v", err)
	}
}

func runPlatformService(cfgMgr *config.Manager) {
	if runtime.GOOS != "windows" {
		log.Printf("platform service mode is currently provided by Windows SCM; use the per-user companion on %s", runtime.GOOS)
		return
	}
	if err := service.RunWindowsService("VKVMCore", func(ctx context.Context) error {
		return runServiceContextMode(cfgMgr, ctx, false)
	}); err != nil {
		log.Printf("VKVM platform service stopped: %v", err)
	}
}

func runServiceContext(cfgMgr *config.Manager, parent context.Context) error {
	return runServiceContextMode(cfgMgr, parent, true)
}

// runServiceContextMode shares the authenticated network core between the
// interactive companion and a platform service. A service session must not
// create a tray, global hooks, or CoreGraphics injection objects.
func runServiceContextMode(cfgMgr *config.Manager, parent context.Context, interactive bool) error {
	log.Println("VKVM Service starting...")
	serviceCtx, cancelService := context.WithCancel(parent)
	defer cancelService()
	serviceStatus := service.NewStatusStore(version)
	serviceStatus.SetState(service.StateRunning, nil, 0)
	defer serviceStatus.SetState(service.StateStopped, nil, 0)

	// Create switcher
	sw, err := switcher.New(cfgMgr)
	if err != nil {
		return fmt.Errorf("failed to create switcher: %w", err)
	}

	// Input trap for host mode
	var inputTrap *input.Trap
	var udpReceiver *network.UDPReceiver
	var injector *input.Injector

	// UDP sender for host mode (low-latency binary input transport)
	var udpSender *network.UDPSender
	var apiServer *api.Server
	var hkMgr *hotkey.Manager
	var inputQueue *input.InjectionQueue
	var companionIPC net.Listener
	var companionIPCSecret []byte
	var companionForwardMu sync.RWMutex
	var companionForwardInput func(protocol.InputPayload) error
	var companionEventsMu sync.Mutex
	var companionEvents []service.IPCEnvelope
	queueCompanionEvent := func(eventType string, payload interface{}) {
		data, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return
		}
		companionEventsMu.Lock()
		if len(companionEvents) >= 512 {
			copy(companionEvents, companionEvents[len(companionEvents)-511:])
			companionEvents = companionEvents[:511]
		}
		companionEvents = append(companionEvents, service.IPCEnvelope{Version: service.IPCProtocolVersion, Type: eventType, Payload: data})
		companionEventsMu.Unlock()
	}
	drainCompanionEvents := func() []service.IPCEnvelope {
		companionEventsMu.Lock()
		events := append([]service.IPCEnvelope(nil), companionEvents...)
		companionEvents = companionEvents[:0]
		companionEventsMu.Unlock()
		return events
	}
	inputMetrics := input.NewMetrics(4096)
	inputState := input.NewStateTracker()
	traceDir := os.Getenv("VKVM_TRACE_DIR")
	var coordinatorTrace *network.TraceRecorder
	var packetTrace *network.PacketTraceRecorder
	if traceDir != "" {
		coordinatorTrace = network.NewTraceRecorder(4096)
		packetTrace = network.NewPacketTraceRecorder(8192)
	}
	var shutdownOnce sync.Once
	releaseRemote := func() {
		if udpSender != nil {
			udpSender.SendReleaseAll()
		}
		if apiServer != nil {
			apiServer.BroadcastReleaseAll()
		}
		inputState.Reset()
	}
	shutdown := func() {
		shutdownOnce.Do(func() {
			// Stop accepting new input first, then release any state that may be
			// held by a remote Agent before transports are closed.
			if inputTrap != nil {
				inputTrap.EnableCapture(false)
				if err := inputTrap.Stop(); err != nil {
					log.Printf("Shutdown: input trap stop failed: %v", err)
				}
			}
			releaseRemote()
			if udpReceiver != nil {
				udpReceiver.Stop()
			}
			if injector != nil {
				if inputQueue != nil {
					inputQueue.Flush()
				}
				if err := injector.ReleaseAll(); err != nil {
					log.Printf("Shutdown: agent input release failed: %v", err)
				}
				if inputQueue != nil {
					inputQueue.Close()
				}
			}
			if udpSender != nil {
				udpSender.Stop()
			}
			if apiServer != nil {
				if err := apiServer.Stop(); err != nil {
					log.Printf("Shutdown: API server stop failed: %v", err)
				}
			}
			if companionIPC != nil {
				if err := companionIPC.Close(); err != nil {
					log.Printf("Shutdown: companion IPC stop failed: %v", err)
				}
				companionIPC = nil
			}
			if len(companionIPCSecret) != 0 {
				if err := service.RemoveIPCSecret(); err != nil {
					log.Printf("Shutdown: companion IPC secret cleanup failed: %v", err)
				}
				companionIPCSecret = nil
			}
			if traceDir != "" {
				if coordinatorTrace != nil {
					if err := coordinatorTrace.Save(filepath.Join(traceDir, "coordinator-trace.json")); err != nil {
						log.Printf("Shutdown: coordinator trace save failed: %v", err)
					}
				}
				if packetTrace != nil {
					if err := packetTrace.Save(filepath.Join(traceDir, "packet-trace.json")); err != nil {
						log.Printf("Shutdown: packet trace save failed: %v", err)
					}
				}
			}
			sw.Close()
			cancelService()
		})
	}

	// The non-interactive core owns the local IPC endpoint. A login companion
	// reads the owner-only short-lived secret and uses read-only diagnostics
	// until the core is ready; the core never needs tray or input-hook access.
	if !interactive {
		baseHandler := service.DiagnosticIPCHandler(version, serviceStatus.Snapshot)
		companionHandler := func(ctx context.Context, request service.IPCEnvelope) (service.IPCEnvelope, error) {
			switch request.Type {
			case "switch":
				var payload struct {
					Profile string `json:"profile"`
				}
				if json.Unmarshal(request.Payload, &payload) != nil || payload.Profile == "" {
					return service.IPCEnvelope{}, errors.New("invalid switch payload")
				}
				if err := sw.SwitchToProfile(payload.Profile); err != nil {
					return service.IPCEnvelope{}, err
				}
				return service.IPCEnvelope{Type: "ok", RequestID: request.RequestID}, nil
			case "input":
				var payload protocol.InputPayload
				if json.Unmarshal(request.Payload, &payload) != nil || payload.Type == "" {
					return service.IPCEnvelope{}, errors.New("invalid input payload")
				}
				companionForwardMu.RLock()
				forward := companionForwardInput
				companionForwardMu.RUnlock()
				if forward == nil {
					return service.IPCEnvelope{}, errors.New("companion input path unavailable")
				}
				if err := forward(payload); err != nil {
					return service.IPCEnvelope{}, err
				}
				return service.IPCEnvelope{Type: "ok", RequestID: request.RequestID}, nil
			case "release_all":
				releaseRemote()
				return service.IPCEnvelope{Type: "ok", RequestID: request.RequestID}, nil
			case "poll_input":
				data, marshalErr := json.Marshal(drainCompanionEvents())
				if marshalErr != nil {
					return service.IPCEnvelope{}, marshalErr
				}
				return service.IPCEnvelope{Type: "events", RequestID: request.RequestID, Payload: data}, nil
			default:
				return baseHandler(ctx, request)
			}
		}
		companionIPC, companionIPCSecret, err = service.StartCoreCompanionIPC(serviceCtx, companionHandler)
		if err != nil {
			return fmt.Errorf("start companion IPC: %w", err)
		}
		log.Printf("Companion IPC listening on %s", service.CompanionIPCDescriptor().Address)
	}

	// Start API server if enabled
	cfg := cfgMgr.Get()
	permissionStatus := input.CurrentPermissions()
	if !permissionStatus.CanCapture() || !permissionStatus.CanInject() {
		log.Printf("Input permissions: accessibility=%v input_monitoring=%v; input paths will remain fail-closed until consent is granted", permissionStatus.Accessibility, permissionStatus.InputMonitoring)
	}
	var autostartMu sync.Mutex
	lastStartOnBoot := !cfg.General.StartOnBoot // force one initial application
	applyAutostart := func() {
		current := cfgMgr.Get()
		autostartMu.Lock()
		changed := current.General.StartOnBoot != lastStartOnBoot
		lastStartOnBoot = current.General.StartOnBoot
		autostartMu.Unlock()
		if !changed {
			return
		}
		var err error
		if current.General.StartOnBoot {
			err = autostart.Enable()
		} else if autostart.IsEnabled() {
			err = autostart.Disable()
		}
		if err != nil {
			log.Printf("Autostart: failed to apply start_on_boot=%v: %v", current.General.StartOnBoot, err)
		}
	}
	applyAutostart()
	cfgMgr.RegisterChangeCallback(applyAutostart)
	var tokenMu sync.Mutex
	lastAPIToken := cfg.General.APIToken
	applyTransportToken := func() {
		current := cfgMgr.Get()
		tokenMu.Lock()
		changed := current.General.APIToken != lastAPIToken
		lastAPIToken = current.General.APIToken
		tokenMu.Unlock()
		if !changed {
			return
		}
		if udpSender != nil {
			udpSender.SetKey(current.General.APIToken)
		}
		if udpReceiver != nil {
			udpReceiver.SetKey(current.General.APIToken)
		}
		sw.SetAgentToken(current.General.APIToken)
		log.Printf("Transport credentials rotated; active sessions will re-authenticate")
	}
	cfgMgr.RegisterChangeCallback(applyTransportToken)
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
		apiServer.SetPermissionProvider(input.CurrentPermissions)
		apiServer.SetServiceStatusProvider(serviceStatus.Snapshot)
		apiServer.SetInputMetrics(inputMetrics)
		apiServer.SetInputStateProvider(inputState.Snapshot)

		// Start UDP sender on same port (TCP and UDP can share port numbers)
		udpSender = network.NewUDPSender(cfg.General.APIPort, cfg.General.APIToken)
		udpSender.SetIdentity(cfg.General.DeviceID, network.NewSessionID(),
			protocol.CapabilityInputMetadata|protocol.CapabilityReliableControl|protocol.CapabilityStateSnapshot|protocol.CapabilityUDP)
		udpSender.SetStateSnapshotProvider(inputState.Snapshot)
		if packetTrace != nil {
			udpSender.SetPacketTrace(packetTrace.Record)
		}
		udpSender.SetDeviceValidator(func(deviceID string) bool {
			return cfgMgr.IsPaired(deviceID)
		})
		if err := udpSender.Start(); err != nil {
			log.Printf("Warning: UDP sender failed to start: %v", err)
			udpSender = nil
		}
		apiServer.SetUDPTransportSelector(func(deviceID string) bool {
			return udpSender != nil && udpSender.HasDevice(deviceID)
		})
		companionForwardMu.Lock()
		companionForwardInput = func(payload protocol.InputPayload) error {
			current := cfgMgr.Get()
			if current.General.Role != "host" || !current.General.USBForwardingEnabled {
				return errors.New("host USB forwarding is disabled")
			}
			if current.General.AgentProfile != "" && current.General.CurrentProfile != current.General.AgentProfile {
				return errors.New("host agent profile is not active")
			}
			inputState.Apply(input.InputEvent{
				Type: payload.Type, DeltaX: payload.DeltaX, DeltaY: payload.DeltaY,
				Button: payload.Button, Pressed: payload.Pressed, KeyCode: payload.KeyCode,
				Modifiers: payload.Modifiers, WheelDelta: payload.WheelDelta,
				Timestamp: payload.Timestamp, DeviceID: payload.DeviceID,
				HardwareID: payload.HardwareID, ScanCode: payload.ScanCode,
				Extended: payload.Extended, E1: payload.E1, Repeat: payload.Repeat,
				Sequence: payload.Sequence, SessionID: payload.SessionID, ClockNS: payload.ClockNS,
			})
			inputMetrics.ObservePayload(payload)
			event := input.InputEvent{
				Type: payload.Type, DeltaX: payload.DeltaX, DeltaY: payload.DeltaY,
				Button: payload.Button, Pressed: payload.Pressed, KeyCode: payload.KeyCode,
				Modifiers: payload.Modifiers, WheelDelta: payload.WheelDelta,
				Timestamp: payload.Timestamp, DeviceID: payload.DeviceID,
				HardwareID: payload.HardwareID, ScanCode: payload.ScanCode,
				Extended: payload.Extended, E1: payload.E1, Repeat: payload.Repeat,
				Sequence: payload.Sequence, SessionID: payload.SessionID, ClockNS: payload.ClockNS,
			}
			if udpSender != nil {
				udpSender.SendInputEvent(event)
			}
			if apiServer != nil {
				apiServer.BroadcastInputEvent(event)
			}
			return nil
		}
		companionForwardMu.Unlock()

		// Wire up switcher -> api broadcast for WebSocket
		sw.SetOnSwitch(func(profileName string) {
			queueCompanionEvent("switch", map[string]string{"profile": profileName})
			// Broadcast the switch event to all connected agents
			// Origin is "host" because this callback is triggered by a local decision/action on the host
			// (or a successfully processed agent request)
			apiServer.BroadcastSwitch(profileName, "host")

			// Local logic for host: control input capture based on active profile
			currentCfg := cfgMgr.Get()
			if currentCfg.General.Role == "host" && inputTrap != nil && currentCfg.General.AgentProfile != "" {
				allowCapture := input.CurrentPermissions().CanCapture() && (profileName == currentCfg.General.AgentProfile)
				log.Printf("Switch to profile '%s', agent profile '%s', allow capture: %v", profileName, currentCfg.General.AgentProfile, allowCapture)
				inputTrap.EnableCapture(allowCapture)
			}
		})

		apiPort := cfg.General.APIPort
		apiSupervisor := service.NewSupervisor(
			func(ctx context.Context) error {
				errCh := make(chan error, 1)
				go func() { errCh <- apiServer.Start(apiPort) }()
				select {
				case err := <-errCh:
					return err
				case <-ctx.Done():
					_ = apiServer.Stop()
					return ctx.Err()
				}
			},
			func(context.Context) error {
				if !apiServer.Healthy() {
					return fmt.Errorf("API listener is not healthy")
				}
				return nil
			},
			service.SupervisorConfig{MinBackoff: 500 * time.Millisecond, MaxBackoff: 30 * time.Second, MaxRestarts: 5, HealthInterval: 5 * time.Second, Status: serviceStatus},
		)
		go func() {
			if err := apiSupervisor.Run(serviceCtx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("API supervisor stopped: %v", err)
			}
		}()

		// Watch for config changes to enable/disable USB forwarding (start/stop input capture)
		cfgMgr.RegisterChangeCallback(func() {
			cfg := cfgMgr.Get()
			// Role changes fail closed: a Host trap must not remain active after
			// switching this process to Agent mode.
			if cfg.General.Role != "host" {
				releaseRemote()
				if inputTrap != nil {
					_ = inputTrap.Stop()
					inputTrap = nil
				}
				return
			}
			if cfg.General.Role == "host" {
				if !cfg.General.USBForwardingEnabled {
					releaseRemote()
					if inputTrap != nil {
						log.Printf("Config: USB forwarding disabled via config, stopping input capture")
						inputTrap.Stop()
						inputTrap = nil
					}
				} else {
					// USB forwarding enabled: ensure input trap exists and is started
					if inputTrap == nil {
						log.Printf("Config: USB forwarding enabled via config, starting input capture")
						inputTrap = input.NewTrap()
						trap := inputTrap
						_ = trap.SetKillSwitch(func() {
							log.Printf("EMERGENCY: input trap kill switch activated")
							trap.EnableCapture(false)
							releaseRemote()
							_ = trap.Stop()
						})
						log.Printf("Host mode: AgentProfile='%s'", cfg.General.AgentProfile)

						// Enable capture only when current profile matches AgentProfile
						// (same logic as initial startup — do NOT capture when Host is the active display)
						if cfg.General.AgentProfile != "" {
							detectedProfile, err := sw.DetectActiveProfile()
							if err == nil && detectedProfile == cfg.General.AgentProfile {
								log.Printf("Config callback: detected profile '%s' matches agent profile, enabling capture", detectedProfile)
								inputTrap.EnableCapture(permissionStatus.CanCapture())
							} else {
								// Current profile is NOT the agent — don't capture input on Host
								log.Printf("Config callback: detected profile '%s' does not match agent profile '%s', capture disabled (will auto-enable on profile switch)", detectedProfile, cfg.General.AgentProfile)
								inputTrap.EnableCapture(false)
							}
						} else if cfg.General.InputCaptureEnabled && permissionStatus.CanCapture() {
							log.Printf("Config callback: No agent profile set, using InputCaptureEnabled=true")
							inputTrap.EnableCapture(true)
						} else {
							// No agent profile and InputCaptureEnabled is false — don't capture
							log.Printf("Config callback: No agent profile set and InputCaptureEnabled=false, capture disabled")
							inputTrap.EnableCapture(false)
						}

						if err := inputTrap.Start(); err == nil {
							trap := inputTrap
							// Broadcast captured events to agents
							go func() {
								for event := range trap.Events() {
									event.DeviceID = cfgMgr.Get().General.DeviceID
									inputState.Apply(event)
									inputMetrics.ObserveReceived(event.Timestamp)
									inputMetrics.SetQueueDepth(len(trap.Events()))
									// Send to every available transport. Each agent enables only
									// its selected input callback, so a UDP-capable agent and a
									// WS-fallback agent can coexist safely.
									if udpSender != nil {
										udpSender.SendInputEvent(event)
									}
									if apiServer != nil {
										apiServer.BroadcastInputEvent(event)
									}
								}
							}()
						} else {
							log.Printf("Config callback: Failed to start input trap: %v", err)
						}
					}
					// Existing traps also need to react immediately to an
					// AgentProfile or InputCaptureEnabled change.
					if inputTrap != nil {
						allowCapture := cfg.General.InputCaptureEnabled && permissionStatus.CanCapture()
						if cfg.General.AgentProfile != "" {
							detectedProfile, err := sw.DetectActiveProfile()
							allowCapture = input.CurrentPermissions().CanCapture() && err == nil && detectedProfile == cfg.General.AgentProfile
						}
						inputTrap.EnableCapture(allowCapture)
					}
				}
			}
		})
	}

	// Hotkey manager
	hkMgr = hotkey.NewManager()
	if interactive {
		if err := hkMgr.Start(); err != nil {
			log.Printf("Warning: Hotkey Engine failed to start: %v", err)
		}
	}

	// Input handling based on role. A paired last-known endpoint is sufficient
	// to bootstrap discovery when DHCP changed the configured address.
	agentCoordinatorAddr := configuredCoordinatorEndpoint(cfg)
	log.Printf("Role: %s, CoordinatorAddr: %s", cfg.General.Role, agentCoordinatorAddr)
	if cfg.General.Role == "agent" {
		sw.EnableAgentNetworkAwareReconnect(true)
		networkMonitor := network.NewPlatformNetworkMonitor(2 * time.Second)
		var endpointMu sync.Mutex
		var updateAgentEndpoint func(string)
		var pendingEndpoint string
		paired := make([]network.PairedEndpoint, 0, len(cfg.General.PairedDevices))
		for _, peer := range cfg.General.PairedDevices {
			if peer.Enabled && peer.Role == "host" {
				paired = append(paired, network.PairedEndpoint{DeviceID: peer.DeviceID, PublicKey: peer.PublicKey, Role: peer.Role, LastKnownEndpoint: peer.LastKnownEndpoint, LastSeenAt: peer.LastSeenAt})
			}
		}
		agentCoordinator := network.NewCoordinator(network.CoordinatorConfig{
			Paired: paired, ConfiguredEndpoint: agentCoordinatorAddr, DiscoveryPort: cfg.General.APIPort,
			Trace: func(event network.TraceEvent) {
				if coordinatorTrace != nil {
					coordinatorTrace.Record(event)
				}
			},
			Probe: func(ctx context.Context, endpoint string) error {
				dialer := net.Dialer{Timeout: 600 * time.Millisecond}
				conn, err := dialer.DialContext(ctx, "tcp", endpoint)
				if err != nil {
					return err
				}
				return conn.Close()
			},
			Connect: func(connectCtx context.Context, endpoint string) error {
				sw.SetAgentCoordinatorEndpoint(endpoint)
				sw.NotifyAgentNetworkChange()
				endpointMu.Lock()
				pendingEndpoint = endpoint
				update := updateAgentEndpoint
				endpointMu.Unlock()
				if update != nil {
					update(endpoint)
				}
				waitCtx, cancel := context.WithTimeout(connectCtx, 1200*time.Millisecond)
				defer cancel()
				if err := sw.WaitForAgentConnection(waitCtx); err != nil {
					return err
				}
				// There is only one automatically selectable paired Host. Persisting
				// its verified endpoint makes the next network transition cheap while
				// keeping discovery identity-bound.
				if len(paired) == 1 && cfgMgr.UpdatePairedEndpoint(paired[0].DeviceID, endpoint, time.Now().UTC()) {
					if err := cfgMgr.Save(); err != nil {
						log.Printf("Agent: failed to persist last-known endpoint: %v", err)
					}
				}
				return nil
			},
		})
		startAgentCoordinator := func() {
			go func() {
				if err := agentCoordinator.Run(serviceCtx, networkMonitor.Changes(serviceCtx)); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("Agent coordinator stopped: %v", err)
				}
			}()
		}

		if interactive {
			// Create input injector
			injector = input.NewInjector()

			// Agent input injection control
			var (
				allowInjection bool
				injectionMutex sync.Mutex
			)

			// Periodic detection of current display state. Injection remains
			// fail-closed until an explicit AgentProfile is configured and the local
			// DDC state matches it.
			go func() {
				ticker := time.NewTicker(1 * time.Second) // Check every second
				defer ticker.Stop()

				for {
					select {
					case <-serviceCtx.Done():
						return
					case <-ticker.C:
					}
					currentCfg := cfgMgr.Get()
					if currentCfg.General.AgentProfile == "" {
						injectionMutex.Lock()
						allowInjection = false
						injectionMutex.Unlock()
						continue
					}
					detectedProfile, err := sw.DetectActiveProfile()
					if err != nil {
						injectionMutex.Lock()
						allowInjection = false
						injectionMutex.Unlock()
						continue
					}

					injectionMutex.Lock()
					oldAllow := allowInjection
					allowInjection = input.CurrentPermissions().CanInject() && (detectedProfile == currentCfg.General.AgentProfile)
					newAllow := allowInjection
					injectionMutex.Unlock()

					if oldAllow != newAllow {
						log.Printf("Agent: Periodic check - detected profile '%s', agent profile '%s', allow injection: %v", detectedProfile, currentCfg.General.AgentProfile, newAllow)
					}
				}
			}()

			// The platform injector is reached through one ordered queue. This keeps
			// UDP and WebSocket handover from racing CoreGraphics calls and lets the
			// queue reject stale sequences/coalesce only mouse motion.
			calibrationState := input.NewCalibrationState(input.Calibration{})
			handleInputNow := func(payload protocol.InputPayload) {
				inputMetrics.ObservePayload(payload)
				injectionStart := time.Now()
				// Check if USB forwarding is enabled from Host config
				currentCfg := cfgMgr.Get()
				calibration := input.Calibration{
					MouseSensitivity:   currentCfg.General.MouseSensitivity,
					WheelPixelsPerUnit: currentCfg.General.WheelPixelsPerUnit,
					NaturalScrolling:   currentCfg.General.NaturalScrolling,
					MouseAcceleration:  currentCfg.General.MouseAcceleration,
				}
				if override, ok := currentCfg.General.DeviceCalibrations[payload.HardwareID]; ok {
					if override.MouseSensitivity > 0 {
						calibration.MouseSensitivity = override.MouseSensitivity
					}
					if override.WheelPixelsPerUnit > 0 {
						calibration.WheelPixelsPerUnit = override.WheelPixelsPerUnit
					}
					if override.NaturalScrolling != nil {
						calibration.NaturalScrolling = *override.NaturalScrolling
					}
					if override.MouseAcceleration != "" {
						calibration.MouseAcceleration = override.MouseAcceleration
					}
				}
				calibrationState.Update(calibration)
				if !currentCfg.General.USBForwardingEnabled {
					return
				}

				// Check if injection is allowed based on profile
				injectionMutex.Lock()
				shouldInject := allowInjection
				injectionMutex.Unlock()

				if !shouldInject || !input.CurrentPermissions().CanInject() {
					return
				}

				// Inject calibrated input on Agent while retaining raw payload metrics.
				calibrated := calibrationState.Apply(input.InputEvent{
					Type: payload.Type, DeltaX: payload.DeltaX, DeltaY: payload.DeltaY,
					WheelDelta: payload.WheelDelta,
				})
				var err error
				switch payload.Type {
				case "mouse_move":
					err = injector.InjectMouseMove(calibrated.DeltaX, calibrated.DeltaY)
				case "mouse_btn":
					err = injector.InjectMouseButton(payload.Button, payload.Pressed)
				case "mouse_wheel":
					err = injector.InjectMouseWheel(calibrated.WheelDelta, 0)
				case "mouse_wheel_h":
					err = injector.InjectMouseWheel(0, calibrated.WheelDelta)
				case "key":
					err = injector.InjectKeyEvent(payload.KeyCode, payload.ScanCode, payload.Extended, payload.E1, payload.Pressed, payload.Modifiers)
				}
				if err != nil {
					inputMetrics.RecordDropped()
					return
				}
				inputMetrics.ObserveInjected(injectionStart)
			}
			inputQueue = input.NewInjectionQueue(1024, handleInputNow)
			handleInputEvent := func(payload protocol.InputPayload) {
				if !inputQueue.Submit(payload) {
					inputMetrics.RecordDropped()
				}
				inputMetrics.SetQueueDepth(inputQueue.Depth())
			}
			handleInput := func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
				handleInputEvent(protocol.InputPayload{Type: eventType, DeltaX: deltaX, DeltaY: deltaY, Button: button, Pressed: pressed, KeyCode: keyCode, Modifiers: modifiers, WheelDelta: wheelDelta, Timestamp: timestamp})
			}

			// Set up switch event handler to control injection based on active profile
			sw.SetAgentSwitchHandler(func(profile string) {
				injectionMutex.Lock()
				defer injectionMutex.Unlock()
				currentCfg := cfgMgr.Get()
				if currentCfg.General.AgentProfile != "" {
					allowInjection = input.CurrentPermissions().CanInject() && (profile == currentCfg.General.AgentProfile)
					log.Printf("Agent: Switch event received for profile '%s', agent profile '%s', allow injection: %v", profile, currentCfg.General.AgentProfile, allowInjection)
				} else {
					// No explicit profile is an ambiguous ownership state. Keep the
					// injection gate closed until the user pairs a profile.
					allowInjection = false
					log.Printf("Agent: No agent profile configured, keeping injection disabled")
				}
			})
			releaseInput := func() {
				if inputQueue != nil {
					inputQueue.Flush()
				}
				if err := injector.ReleaseAll(); err != nil {
					log.Printf("Agent: failed to release remote input state: %v", err)
				}
			}
			sw.SetAgentReleaseHandler(releaseInput)
			applyInputState := func(snapshot protocol.InputStatePayload) {
				if inputQueue != nil {
					inputQueue.Flush()
				}
				releaseInput()
				for _, key := range snapshot.Keys {
					if err := injector.InjectKeyEvent(key.KeyCode, key.ScanCode, key.Extended, key.E1, true, snapshot.Modifiers); err != nil {
						log.Printf("Agent: failed to restore key 0x%X: %v", key.KeyCode, err)
					}
				}
				for _, button := range snapshot.Buttons {
					if err := injector.InjectMouseButton(button, true); err != nil {
						log.Printf("Agent: failed to restore mouse button %d: %v", button, err)
					}
				}
			}
			sw.SetAgentInputStateHandler(applyInputState)

			setWebSocketInput := func(callback func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64)) {
				sw.SetAgentInputHandler(callback)
			}
			setWebSocketInputEvent := func(callback func(protocol.InputPayload)) {
				sw.SetAgentInputEventHandler(callback)
			}

			// UDP is bound to the currently matched coordinator endpoint. When the
			// network fingerprint changes, the coordinator calls this function to
			// tear down the old socket, probe the new endpoint, and atomically switch
			// the input callbacks. This prevents a stale DHCP address from keeping
			// the agent permanently on a dead UDP path.
			var agentUDPMu sync.Mutex
			configureUDP := func(endpoint string) {
				if endpoint == "" {
					inputQueue.SetActiveSession(sw.AgentSessionID())
					setWebSocketInputEvent(handleInputEvent)
					setWebSocketInput(handleInput)
					return
				}
				agentUDPMu.Lock()
				defer agentUDPMu.Unlock()
				old := udpReceiver
				udpReceiver = nil
				if old != nil {
					old.Stop()
				}
				receiver := network.NewUDPReceiver(endpoint, cfgMgr.Get().General.APIToken, cfgMgr.Get().General.DeviceID)
				if packetTrace != nil {
					receiver.SetPacketTrace(packetTrace.Record)
				}
				for _, peer := range cfgMgr.Get().General.PairedDevices {
					if peer.Enabled && peer.Role == "host" {
						receiver.SetExpectedPeerDeviceID(peer.DeviceID)
						sw.SetAgentExpectedPeerDeviceID(peer.DeviceID)
						break
					}
				}
				receiver.OnTransportLost = func() {
					agentUDPMu.Lock()
					current := udpReceiver == receiver
					if current {
						udpReceiver = nil
					}
					agentUDPMu.Unlock()
					if !current {
						return
					}
					log.Printf("Agent: UDP transport lost; falling back to authenticated WebSocket")
					inputQueue.SetActiveSession(sw.AgentSessionID())
					sw.SetAgentTransportCapabilities(protocol.CapabilityInputMetadata | protocol.CapabilityReliableControl | protocol.CapabilityStateSnapshot)
					setWebSocketInputEvent(handleInputEvent)
					setWebSocketInput(handleInput)
					receiver.Stop()
				}
				if !receiver.Probe() {
					log.Printf("Agent: UDP probe failed for %s, using WebSocket for input", endpoint)
					inputQueue.SetActiveSession(sw.AgentSessionID())
					setWebSocketInputEvent(handleInputEvent)
					setWebSocketInput(handleInput)
					return
				}
				receiver.OnInputEvent = func(payload protocol.InputPayload) { handleInputEvent(payload) }
				receiver.OnInput = handleInput
				receiver.OnInputState = applyInputState
				receiver.OnReleaseAll = releaseInput
				if err := receiver.Start(); err != nil {
					log.Printf("Warning: UDP receiver start failed for %s: %v (using WebSocket)", endpoint, err)
					inputQueue.SetActiveSession(sw.AgentSessionID())
					setWebSocketInputEvent(handleInputEvent)
					setWebSocketInput(handleInput)
					return
				}
				udpReceiver = receiver
				if peerSession := receiver.PeerSessionID(); peerSession != "" {
					inputQueue.SetActiveSession(peerSession)
				}
				log.Printf("Agent: UDP transport active for %s", endpoint)
				sw.SetAgentTransportCapabilities(protocol.CapabilityInputMetadata | protocol.CapabilityReliableControl | protocol.CapabilityStateSnapshot | protocol.CapabilityUDP)
				setWebSocketInputEvent(nil)
				setWebSocketInput(nil)
			}
			updateAgentEndpoint = func(endpoint string) { go configureUDP(endpoint) }
			endpointMu.Lock()
			initialEndpoint := pendingEndpoint
			if initialEndpoint == "" {
				initialEndpoint = agentCoordinatorAddr
			}
			endpointMu.Unlock()
			if initialEndpoint != "" {
				configureUDP(initialEndpoint)
			} else {
				inputQueue.SetActiveSession(sw.AgentSessionID())
				setWebSocketInputEvent(handleInputEvent)
				setWebSocketInput(handleInput)
			}
		} else {
			// A service-mode Agent owns the authenticated network session but has
			// no interactive injector. Queue remote input/state transitions for the
			// logged-in companion to poll over IPC.
			sw.SetAgentInputEventHandler(func(payload protocol.InputPayload) {
				queueCompanionEvent("input", payload)
			})
			sw.SetAgentInputStateHandler(func(snapshot protocol.InputStatePayload) {
				queueCompanionEvent("input_state", snapshot)
			})
			sw.SetAgentReleaseHandler(func() { queueCompanionEvent("release_all", nil) })
			sw.SetAgentSwitchHandler(func(profile string) {
				queueCompanionEvent("switch", map[string]string{"profile": profile})
			})
		}
		// Start discovery only after the interactive transport callbacks have
		// been installed. Otherwise a fast initial fingerprint can complete
		// discovery before updateAgentEndpoint is assigned, leaving the Agent on
		// an unintended transport until the next network change.
		startAgentCoordinator()
	} else if cfg.General.Role == "host" {
		// Check administrator privileges on Windows
		if runtime.GOOS == "windows" {
			log.Println("Note: Input capture requires administrator privileges")
			log.Println("Please ensure you're running this application as Administrator")
		}

		// Start input capture on host (only if USB forwarding is enabled)
		log.Printf("Host mode: USB Forwarding Enabled: %v", cfg.General.USBForwardingEnabled)
		if interactive && cfg.General.USBForwardingEnabled {
			inputTrap = input.NewTrap()
			trap := inputTrap
			_ = trap.SetKillSwitch(func() {
				log.Printf("EMERGENCY: input trap kill switch activated")
				trap.EnableCapture(false)
				releaseRemote()
				_ = trap.Stop()
			})

			log.Printf("Host mode: AgentProfile='%s'", cfg.General.AgentProfile)

			// Initial capture state: check current profile
			if cfg.General.AgentProfile != "" {
				detectedProfile, err := sw.DetectActiveProfile()
				if err == nil && detectedProfile == cfg.General.AgentProfile && input.CurrentPermissions().CanCapture() {
					log.Printf("Initial profile '%s' matches agent profile, enabling capture", detectedProfile)
					inputTrap.EnableCapture(true)
				} else {
					log.Printf("Initial profile '%s' does not match agent profile '%s', capture disabled", detectedProfile, cfg.General.AgentProfile)
				}
			} else if cfg.General.InputCaptureEnabled && permissionStatus.CanCapture() {
				// Fallback to config if no agent profile set
				log.Printf("No agent profile set, using config InputCaptureEnabled: %v", cfg.General.InputCaptureEnabled)
				inputTrap.EnableCapture(true)
			} else {
				log.Printf("Input capture not enabled")
			}

			if err := inputTrap.Start(); err == nil {
				trap := inputTrap
				// Process captured events and broadcast to all connected agents
				go func() {
					for event := range trap.Events() {
						event.DeviceID = cfgMgr.Get().General.DeviceID
						inputState.Apply(event)
						inputMetrics.ObserveReceived(event.Timestamp)
						inputMetrics.SetQueueDepth(len(trap.Events()))
						// Send to every available transport. UDP-capable agents
						// receive the low-latency path; WS-only agents remain
						// connected independently.
						if udpSender != nil {
							udpSender.SendInputEvent(event)
						}
						if apiServer != nil {
							apiServer.BroadcastInputEvent(event)
						}
					}
				}()
			}
		} else {
			log.Printf("USB forwarding disabled, skipping input capture")
		}
	}

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
		if !interactive {
			return
		}
		cfg := cfgMgr.Get()
		hkMgr.Clear() // Clear existing registered callbacks

		// Register emergency escape hotkey for Host with input capture
		if cfg.General.Role == "host" && cfg.General.EscapeHotkey != "" {
			_, err := hkMgr.Register(cfg.General.EscapeHotkey, func() {
				log.Printf("EMERGENCY: Escape hotkey pressed - disabling input capture")
				if inputTrap != nil {
					inputTrap.EnableCapture(false)
				}
				releaseRemote()
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
	if cfg.General.Role == "agent" && agentCoordinatorAddr != "" {
		log.Printf("Service: Initial sync from Host %s...", agentCoordinatorAddr)
		// One immediate sync on startup (synchronous)
		if err := sw.SyncProfiles(); err == nil {
			refreshShortcuts()
		} else {
			log.Printf("Warning: Initial sync from Host failed: %v", err)
		}

		go func() {
			// Periodic sync every 2 minutes
			ticker := time.NewTicker(2 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-serviceCtx.Done():
					return
				case <-ticker.C:
					if err := sw.SyncProfiles(); err == nil {
						refreshShortcuts()
					}
				}
			}
		}()
	}

	if !interactive {
		<-serviceCtx.Done()
		shutdown()
		return serviceCtx.Err()
	}

	// Tray instance
	t := tray.New("VKVM - KVM Switcher")

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
		shutdown()
		t.Stop()
	})

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		shutdown()
		t.Stop()
	}()

	log.Println("VKVM Service running. Press Ctrl+C to stop.")
	t.Run()
	// systray may return because the session is ending rather than through the
	// explicit Quit callback; cover that path as well.
	shutdown()
	return nil
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

func configuredCoordinatorEndpoint(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.General.CoordinatorAddr != "" {
		return cfg.General.CoordinatorAddr
	}
	for _, peer := range cfg.General.PairedDevices {
		if peer.Enabled && peer.Role == "host" && peer.LastKnownEndpoint != "" {
			return peer.LastKnownEndpoint
		}
	}
	return ""
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
		wsClient = network.NewWSClient(cfg.General.CoordinatorAddr, cfg.General.APIToken, cfg.General.DeviceID)

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
		wsClient = network.NewWSClient(cfg.General.CoordinatorAddr, cfg.General.APIToken, cfg.General.DeviceID)

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
