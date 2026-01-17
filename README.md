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
- **Administrator Rights**: May be required for firewall rule creation
- **DDC/CI Enabled**: Enable in your monitor's OSD settings

## Installation

### From Source
```bash
# Clone the repository
git clone https://github.com/yourusername/vkvm.git
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

### Configuration

1. **Open Settings**: Click the tray icon → "Settings..."
2. **Add Profiles**: Create profiles for each computer (e.g., "PC1", "Mac")
3. **Set Hotkeys**: Click "🔴 Record" to capture key/mouse combinations
4. **Configure Monitors**: Select the input source for each monitor per profile

### Network Setup (Multi-Computer)

1. **Host Machine** (main controller):
   - Set Role: `Host`
   - Enable API Server

2. **Agent Machines** (controlled computers):
   - Set Role: `Agent`
   - Enter Host's IP:Port in "Coordinator Address"
   
Agent machines will auto-sync profiles from the Host.

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

### Windows: "DDC Supported" shows incorrectly in UI
> ℹ️ **Known Issue**: On Windows, the DDC support detection may show "No" even for monitors that actually support DDC/CI. **This does not affect switching functionality** – if your monitor supports DDC, the input switching will still work correctly.

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
