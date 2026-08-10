#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
OUT=${TMPDIR:-/tmp}/vkvm-dmg-smoke
rm -rf "$OUT"
mkdir -p "$OUT"
BUILD_LOG="$OUT/build.log"
if GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/vkvm-go-build-cache}" "$ROOT/packaging/macos/build-dmg.sh" "$OUT" >"$BUILD_LOG" 2>&1; then
	test -s "$OUT/VKVM.dmg"
	hdiutil imageinfo "$OUT/VKVM.dmg" | grep -q 'Format:.*UDZO'
	echo "macOS DMG bundle smoke test passed"
else
	# Some restricted macOS runners expose hdiutil but have no writable device.
	# Still verify the complete app bundle and leave the real DMG gate to the
	# release runner where a disk image device is available.
	if ! grep -Eqi 'no device|device unavailable|尚未設定裝置|resource temporarily unavailable' "$BUILD_LOG"; then
		cat "$BUILD_LOG" >&2
		exit 1
	fi
	rm -rf "$OUT/stage"
	GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/vkvm-go-build-cache}" "$ROOT/packaging/macos/build-dmg.sh" "$OUT" --portable
  test -s "$OUT/VKVM.app.tar.gz"
  tar -tzf "$OUT/VKVM.app.tar.gz" | grep -q 'VKVM.app/Contents/MacOS/vkvm'
  echo "macOS app bundle smoke test passed (DMG device unavailable)"
fi
