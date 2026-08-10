#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MODE=${1:-portable}
GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/vkvm-go-build-cache}
export GOCACHE

cd "$ROOT"
go test ./internal/service ./internal/network -run 'Supervisor|Coordinator|Trace|IPC' -count=1
./packaging/verify-package.sh
./packaging/windows/test-installer.sh

if [ "$MODE" = "portable" ]; then
  echo "platform lifecycle checks passed (portable; SCM/LaunchAgent/TCC not claimed)"
  exit 0
fi
if [ "$MODE" != "real" ]; then
  echo "usage: $0 [portable|real]" >&2
  exit 2
fi

case "$(go env GOOS)" in
  darwin)
    plist="$HOME/Library/LaunchAgents/com.vkvm.agent.plist"
    if [ ! -f "$plist" ]; then
      echo "LaunchAgent plist not installed: $plist" >&2
      exit 1
    fi
    if ! grep -q '<string>--companion</string>' "$plist"; then
      echo "LaunchAgent plist does not launch --companion" >&2
      exit 1
    fi
    command -v launchctl >/dev/null 2>&1
    launchctl print "gui/$(id -u)/com.vkvm.agent" >/dev/null
    ;;
  windows)
    command -v powershell.exe >/dev/null 2>&1
    powershell.exe -NoProfile -NonInteractive -Command "\$s=Get-Service -Name VKVMCore -ErrorAction Stop; if (\$s.Status -ne 'Running') { exit 1 }"
    ;;
  *)
    echo "real platform acceptance is only defined for Windows/macOS" >&2
    exit 2
    ;;
esac
echo "real service lifecycle evidence found"
