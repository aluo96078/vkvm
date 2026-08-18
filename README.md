# VKVM - Virtual KVM Switcher

[📖 繁體中文版文檔 / Chinese Documentation](./README_zh-TW.md)

A cross-platform DDC/CI-based monitor input switcher that works like a software KVM switch.

## Motivation

Hardware KVM switches that support 2+ monitors and 2+ computers are prohibitively expensive. VKVM provides a free, software-based alternative that leverages DDC/CI to control monitor inputs directly, eliminating the need for costly multi-monitor KVM hardware.

## Tested Environment

| Component | Specification |
|-----------|---------------|
| **macOS** | M4 Pro MacBook Pro (1× Native HDMI + 1× USB-C Hub) |
| **Windows** | PC with RTX 3090 GPU |
| **Monitors** | ASUS VG27AQL3A-W × 2 |

> ⚠️ **Important**: DDC/CI must be enabled in your monitor's OSD settings for VKVM to work.

## Features

- 🖥️ **DDC/CI Monitor Control** - Switch monitor inputs without physical KVM hardware
- ⌨️ **Global Hotkeys** - Use keyboard shortcuts or mouse button combinations (e.g., `Mouse2+Mouse3`)
- 🌐 **Network Switching** - Control multiple computers over LAN with Host/Agent architecture
- 🔄 **Cross-Platform Hotkey Mapping** - `Ctrl+X` hotkeys auto-map to `Cmd+X` on macOS
- 💤 **Auto Wake** - Simulates mouse movement to wake sleeping monitors before switching

## Prerequisites

### macOS
- **Accessibility Permissions**: Required for global hotkey detection
  - Go to `System Settings` → `Privacy & Security` → `Accessibility`
  - Add `vkvm` to the allowed apps list
- **DDC Support**: Works with most external monitors via DDC/CI

### Windows
- **ControlMyMonitor**: Bundled automatically (no separate install needed)
- **Administrator Rights**: If the API port is not allowed, VKVM asks for UAC permission after the app starts to create the firewall rule
- **DDC/CI Enabled**: Enable in your monitor's OSD settings

## Installation

### From Source
```bash
# Clone the repository
git clone https://github.com/aluo96078/vkvm.git
cd vkvm

# Build for current platform
go build -o vkvm ./cmd

# Build for Windows (without console window)
GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui" -o vkvm.exe ./cmd

# Build for macOS
GOOS=darwin GOARCH=arm64 go build -o vkvm ./cmd
```

## Usage

### Basic Usage
```bash
# Start the service (runs in system tray)
./vkvm

# Open configuration UI
./vkvm -ui
```

### System Tray Usage

VKVM runs in the system tray (menu bar on macOS, taskbar notification area on Windows). Right-click the tray icon to access the menu.

#### macOS

![macOS System Tray Menu](./docs/mac/taskbar.png)

The menu bar icon provides quick access to:
- **Switch profiles** - Click any profile name to switch immediately
- **Settings...** - Open the configuration UI
- **Quit** - Exit VKVM

#### Windows

![Windows System Tray Menu](./docs/windows/taskbar.png)

Right-click the system tray icon to:
- **Switch profiles** - Select any profile to switch
- **Settings...** - Open the configuration UI
- **Quit** - Exit VKVM

> 💡 **Tip**: On Windows, you may need to click the "^" arrow in the taskbar to find the VKVM icon if it's hidden in the overflow area.

### Configuration

1. **Open Settings**: Click the tray icon → "Settings..."
2. **Add Profiles**: Create profiles for each computer (e.g., "PC1", "Mac")
3. **Set Hotkeys**: Click "🔴 Record" to capture key/mouse combinations
4. **Configure Monitors**: Select the input source for each monitor per profile

### Network Setup (Multi-Computer)

1. **Host Machine** (main controller):
   - Set Role: `Host`
   - Explicitly enable the API server
   - Set a high-entropy `API Token`; the API will not start without one

2. **Agent Machines** (controlled computers):
   - Set Role: `Agent`
   - Enter Host's IP:Port in "Coordinator Address"
   - Use the same API Token as the Host
   
Agent machines will auto-sync profiles from the Host.

> ⚠️ API access and USB input forwarding are disabled by default. Enable them only after pairing the machines on a trusted network; HTTP, WebSocket, and UDP control paths all require authentication.

`Start on boot` installs the interactive user-session launcher (macOS
LaunchAgent or Windows per-user Run entry). The service keeps the agent's
authenticated WebSocket dormant when its coordinator is not reachable and
wakes it after a network fingerprint change; UDP heartbeat loss automatically
falls back to WebSocket. The current forwarding path is software input
capture/injection, not a USB/IP device and cannot emulate Secure Input or a
real HID descriptor.

On Windows, the installer also provides a non-interactive `VKVMCore` SCM
service (`vkvm.exe --service`) for authenticated networking and supervision;
the per-user `vkvm.exe --companion` process remains responsible for tray, hooks
and interactive permissions. It does not bind a second API/UDP listener.

The USB/BLE feasibility decision and hardware-gated follow-up path are
documented in [`docs/USB_HID_BLE_FEASIBILITY.md`](docs/USB_HID_BLE_FEASIBILITY.md).
See [`docs/INSTALL.md`](docs/INSTALL.md) for setup and
[`docs/API.md`](docs/API.md) for the authenticated API surface.

Changes to the API port, role, coordinator address, or API token take effect
on the next service restart; the UI preserves the existing token when its
redacted configuration snapshot is saved.

Authenticated `GET /api/metrics` returns bounded receive/injection counters,
queue depth, p50/p95/p99 samples, clock/path-offset samples, jitter and
sequence-gap loss. Acceptance thresholds remain operator-supplied from a
real Windows/macOS hardware baseline.

Agent discovery is pairing-first: it tries each paired last-known endpoint,
then performs one bounded `_vkvm._tcp` mDNS lookup after boot or a network
fingerprint change. If no single paired UUID/public-key match is available it
enters `WaitingForNetworkChange` instead of continuously scanning the LAN.

## Hotkey Examples

| Hotkey | Description |
|--------|-------------|
| `Ctrl+Shift+F1` | Switch to Profile 1 |
| `Mouse2+Mouse3` | Middle + Right mouse buttons |
| `Ctrl+Alt+1` | Standard modifier combo |

> **Note**: On macOS, `Ctrl+X` hotkeys also respond to `Cmd+X`

## Architecture

```
┌─────────────────┐         ┌─────────────────┐
│   Host (Win)    │◄───────►│   Agent (Mac)   │
│   DDC + API     │  HTTP   │   DDC + API     │
└─────────────────┘         └─────────────────┘
        │                           │
        ▼                           ▼
   ┌─────────┐                 ┌─────────┐
   │ Monitor │                 │ Monitor │
   └─────────┘                 └─────────┘
```

## Troubleshooting

### macOS: Hotkeys not working
- Grant Accessibility permissions in System Settings
- Restart the application after granting permissions

### Windows: DDC commands fail
- Enable DDC/CI in your monitor's OSD menu
- Try running as Administrator
- Check if ControlMyMonitor works manually

### Network: Agent can't connect to Host
- Verify Host's API server is enabled
- Check firewall settings on Host (port 18080 by default)
- Ensure both machines are on the same network

### DDC not working with USB-C/Thunderbolt adapters
> ⚠️ **Important**: Many USB-C/Thunderbolt to HDMI/DisplayPort adapters do NOT support DDC/CI passthrough. This is a hardware limitation.

- Try using a direct HDMI or DisplayPort cable instead
- If you must use an adapter, look for one that explicitly supports DDC/CI
- Some docking stations also block DDC signals

**Workaround**: If your adapter doesn't support DDC, you can still use VKVM by setting up **Host/Agent mode**. Configure the computer with DDC-capable connections as the **Host**, and the computer with the non-DDC adapter as an **Agent**. When you trigger a switch, the Host will send DDC commands to the monitors while the Agent receives a network notification.

## Acknowledgements

VKVM relies on the following excellent open-source tools for DDC/CI communication:

| Platform | Tool | Author | License |
|----------|------|--------|--------|
| **macOS** | [m1ddc](https://github.com/waydabber/m1ddc) | @waydabber | MIT |
| **Windows** | [ControlMyMonitor](https://www.nirsoft.net/utils/control_my_monitor.html) | NirSoft | Freeware |

> **m1ddc** - A command-line tool for controlling Apple Silicon Mac displays via DDC/CI.
>
> **ControlMyMonitor** - A Windows utility for viewing and modifying monitor settings using DDC/CI protocol.

Without these tools, VKVM would not be possible. Thank you to their respective authors!

## License

MIT License
