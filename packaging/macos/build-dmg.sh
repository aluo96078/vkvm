#!/bin/sh
set -eu

# Reproducible local packaging helper. Signing/notarization are deliberately
# supplied by the release environment through CODESIGN_IDENTITY and
# NOTARY_PROFILE; a developer build remains unsigned and cannot claim a
# trusted distribution artifact.
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
OUT=${1:-"$ROOT/dist"}
PORTABLE=0
if [ "${2:-}" = "--portable" ]; then
  PORTABLE=1
fi
cd "$ROOT"
VERSION=$(tr -d '[:space:]' < "$ROOT/VERSION")
if [ -z "${GOCACHE:-}" ]; then
  GOCACHE="${TMPDIR:-/tmp}/vkvm-go-build-cache"
  export GOCACHE
fi
mkdir -p "$OUT/stage/VKVM.app/Contents/MacOS"
go build -trimpath -o "$OUT/stage/VKVM.app/Contents/MacOS/vkvm" "$ROOT/cmd"
cat > "$OUT/stage/VKVM.app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.vkvm.app</string>
<key>CFBundleName</key><string>VKVM</string>
<key>CFBundleExecutable</key><string>vkvm</string>
<key>CFBundleVersion</key><string>$VERSION</string>
</dict></plist>
PLIST
if [ -n "${CODESIGN_IDENTITY:-}" ]; then
  codesign --force --options runtime --timestamp --sign "$CODESIGN_IDENTITY" "$OUT/stage/VKVM.app"
fi
if [ "$PORTABLE" -eq 1 ]; then
  tar -czf "$OUT/VKVM.app.tar.gz" -C "$OUT/stage" VKVM.app
  echo "created $OUT/VKVM.app.tar.gz"
  exit 0
fi
hdiutil create -volname VKVM -srcfolder "$OUT/stage" -ov -format UDZO "$OUT/VKVM.dmg"
if [ -n "${NOTARY_PROFILE:-}" ]; then
  xcrun notarytool submit "$OUT/VKVM.dmg" --keychain-profile "$NOTARY_PROFILE" --wait
  xcrun stapler staple "$OUT/VKVM.dmg"
fi
echo "created $OUT/VKVM.dmg"
