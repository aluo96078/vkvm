#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MODE=${1:-portable}
GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/vkvm-go-build-cache}
export GOCACHE

cd "$ROOT"
go test ./internal/hid ./internal/integration -run 'HID|BLE|Authenticated' -count=1
if command -v cc >/dev/null 2>&1; then
  cc -std=c11 -Wall -Wextra -Werror -fsyntax-only hardware/esp32s3/vkvm_hid_device.c -I hardware/esp32s3
fi

if [ "$MODE" = "portable" ]; then
  echo "HID contract checks passed (portable; no enumeration claim)"
  exit 0
fi
if [ "$MODE" != "real" ]; then
  echo "usage: $0 [portable|real]" >&2
  exit 2
fi

expected=${VKVM_HID_EXPECTED_NAME:-VKVM}
case "$(go env GOOS)" in
  darwin)
    command -v system_profiler >/dev/null 2>&1
    system_profiler SPUSBDataType >"${TMPDIR:-/tmp}/vkvm-usb.txt"
    if ! grep -F "$expected" "${TMPDIR:-/tmp}/vkvm-usb.txt" >/dev/null; then
      echo "expected USB HID device '$expected' was not enumerated" >&2
      exit 1
    fi
    command -v hidutil >/dev/null 2>&1
    if ! hidutil list | grep -F "$expected" >/dev/null; then
      echo "expected HID device '$expected' is absent from hidutil" >&2
      exit 1
    fi
    ;;
  windows)
    command -v powershell.exe >/dev/null 2>&1
    powershell.exe -NoProfile -NonInteractive -Command "Get-PnpDevice -Class HIDClass | Where-Object { \$_.FriendlyName -like '*$expected*' } | Out-Null"
    ;;
  *)
    echo "real HID acceptance is only defined for Windows/macOS" >&2
    exit 2
    ;;
esac
echo "real HID enumeration evidence found for $expected"
