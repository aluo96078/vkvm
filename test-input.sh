#!/bin/bash

# VKVM Input Forwarding Test Script
# This script helps set up and run input forwarding tests

set -e

# Check if running on Windows without proper bash environment
if [[ "$OSTYPE" == "msys" ]] || [[ "$OSTYPE" == "win32" ]]; then
    echo "ERROR: This .sh script cannot run directly on Windows."
    echo ""
    echo "Windows users should use one of these alternatives:"
    echo ""
    echo "1. Batch file (simplest):"
    echo "   test-input.bat setup"
    echo "   test-input.bat test"
    echo ""
    echo "2. PowerShell script (full featured):"
    echo "   .\test-input.ps1 setup"
    echo "   .\test-input.ps1 test"
    echo ""
    echo "For more information, see WINDOWS_TEST_README.md"
    exit 1
fi

echo "VKVM Input Forwarding Test Setup"
echo "================================"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if we're on the right platform
case "$(uname -s)" in
    Darwin)
        PLATFORM="macos"
        ;;
    CYGWIN*|MINGW32*|MSYS*|MINGW*)
        PLATFORM="windows"
        ;;
    *)
        echo -e "${RED}Unsupported platform: $(uname -s)${NC}"
        exit 1
        ;;
esac

# Function to check for hotkey conflicts (Windows only)
check_hotkey_conflicts() {
    if [ "$PLATFORM" != "windows" ]; then
        return
    fi

    echo -e "${YELLOW}Checking for hotkey conflicts...${NC}"

    # Check if PowerShell script exists
    if [ -f "check-hotkey-conflicts.ps1" ]; then
        echo "Running hotkey conflict checker..."
        powershell.exe -ExecutionPolicy Bypass -File check-hotkey-conflicts.ps1
    else
        echo "Hotkey conflict checker not found. Checking common conflicting processes..."

        # Check for common conflicting processes
        conflicting_processes=("barrier" "synergy" "teamviewer" "anydesk" "obs64" "obs32" "bandicam")

        found_conflicts=false
        for proc in "${conflicting_processes[@]}"; do
            if tasklist 2>/dev/null | grep -i "$proc" >/dev/null 2>&1; then
                echo -e "${RED}Found potentially conflicting process: $proc${NC}"
                found_conflicts=true
            fi
        done

        if [ "$found_conflicts" = false ]; then
            echo -e "${GREEN}No obvious conflicting processes found.${NC}"
        fi
    fi

    echo -e "${YELLOW}If you encounter hotkey registration errors, try closing conflicting applications.${NC}"
    echo ""
}

# Function to setup Windows
setup_windows() {
    echo -e "${YELLOW}Setting up Windows agent (input capture)...${NC}"

    # Check for hotkey conflicts first
    check_hotkey_conflicts

    # Get macOS IP
    read -p "Enter macOS computer IP address: " MACOS_IP

    # Check if config exists
    if [ ! -f "config.json" ]; then
        echo "Creating default config.json..."
        cat > config.json << EOF
{
  "general": {
    "role": "agent",
    "coordinator_addr": "$MACOS_IP:8080",
    "api_token": "test_token_123"
  }
}
EOF
    fi

    echo -e "${GREEN}Windows setup complete. Run: vkvm-windows-test.exe -test-input${NC}"
}

# Function to setup macOS
setup_macos() {
    echo -e "${YELLOW}Setting up macOS host (input injection)...${NC}"

    # Check if config exists
    if [ ! -f "config.json" ]; then
        echo "Creating default config.json..."
        cat > config.json << EOF
{
  "general": {
    "role": "host",
    "api_listen_addr": "0.0.0.0:8080",
    "api_token": "test_token_123"
  }
}
EOF
    fi

    echo -e "${GREEN}macOS setup complete. Run: ./vkvm-macos-test -test-input${NC}"
}

# Function to run test
run_test() {
    echo -e "${YELLOW}Running input forwarding test...${NC}"

    case "$PLATFORM" in
        windows)
            echo "Starting Windows input capture..."
            echo "Press Ctrl+Alt+Esc to stop"
            ./vkvm-windows-test.exe -test-input
            ;;
        macos)
            echo "Starting macOS input injection..."
            echo "Press Ctrl+C to stop"
            ./vkvm-macos-test -test-input
            ;;
    esac
}

# Function to show help
show_help() {
    echo "Usage: $0 [COMMAND]"
    echo ""
    echo "Commands:"
    echo "  setup    - Setup configuration for current platform"
    echo "  test     - Run input forwarding test"
    echo "  help     - Show this help"
    echo ""
    echo "Examples:"
    echo "  $0 setup    # Setup config"
    echo "  $0 test     # Run test"
}

# Main logic
case "${1:-help}" in
    setup)
        case "$PLATFORM" in
            windows)
                setup_windows
                ;;
            macos)
                setup_macos
                ;;
        esac
        ;;
    test)
        run_test
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        echo -e "${RED}Unknown command: $1${NC}"
        show_help
        exit 1
        ;;
esac