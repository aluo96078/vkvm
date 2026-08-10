#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test -x "$ROOT/packaging/macos/build-dmg.sh"
test -x "$ROOT/packaging/macos/test-dmg.sh"
test -x "$ROOT/packaging/coverage-gate.sh"
test -x "$ROOT/packaging/platform-acceptance.sh"
test -x "$ROOT/packaging/network-trace.sh"
test -x "$ROOT/hardware/accept-hid.sh"
test -f "$ROOT/packaging/windows/vkvm.nsi"
test -f "$ROOT/packaging/windows/install-service.ps1"
test -f "$ROOT/packaging/windows/vkvm.wxs"
test -f "$ROOT/VERSION"
test -f "$ROOT/RELEASE_NOTES.md"
grep -q 'VKVMCore' "$ROOT/packaging/windows/vkvm.nsi"
grep -q 'delayed-auto' "$ROOT/packaging/windows/install-service.ps1"
echo "portable packaging metadata verified"
