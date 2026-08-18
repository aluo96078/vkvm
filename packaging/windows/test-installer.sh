#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
test -s "$ROOT/VERSION"
grep -q 'VKVMCore' "$ROOT/packaging/windows/vkvm.nsi"
grep -q 'delayed-auto' "$ROOT/packaging/windows/vkvm.nsi"
grep -q 'failure' "$ROOT/packaging/windows/install-service.ps1"
grep -q -- '--service' "$ROOT/packaging/windows/install-service.ps1"
grep -q -- '--service' "$ROOT/packaging/windows/vkvm.nsi"
grep -q 'CreateShortCut' "$ROOT/packaging/windows/vkvm.nsi"
grep -q 'CurrentVersion\\\\Run' "$ROOT/packaging/windows/vkvm.nsi"
grep -q -- '--enable-autostart' "$ROOT/packaging/windows/vkvm.nsi"
grep -q 'WriteUninstaller' "$ROOT/packaging/windows/vkvm.nsi"
grep -q 'sc.exe.*start VKVMCore' "$ROOT/packaging/windows/vkvm.nsi"
grep -q 'InstallLocation' "$ROOT/packaging/windows/vkvm.nsi"
grep -q -- '--companion' "$ROOT/internal/autostart/autostart_windows.go"
grep -q '<ServiceInstall' "$ROOT/packaging/windows/vkvm.wxs"
grep -q 'ProductVersion' "$ROOT/packaging/windows/vkvm.wxs"
grep -q 'build .*ProductVersion' "$ROOT/packaging/windows/build-msi.ps1"
if command -v makensis >/dev/null 2>&1 && [ -s "$ROOT/dist/vkvm.exe" ]; then
  (cd "$ROOT" && makensis packaging/windows/vkvm.nsi >/dev/null)
  test -s "$ROOT/VKVM-Setup.exe"
fi
echo "Windows installer metadata smoke test passed"
