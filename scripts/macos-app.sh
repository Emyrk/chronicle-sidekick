#!/usr/bin/env bash
# Wrap the universal darwin binary in a Chronicle Sidekick.app and zip it.
# Usage: macos-app.sh <binary> <version> <out.zip>
set -euo pipefail
bin="$1" version="$2" out="$3"
script_dir="$(cd "$(dirname "$0")" && pwd)"
: "${SPARKLE_DIR:?run scripts/setup-sparkle.sh and set SPARKLE_DIR}"
SPARKLE_PUBLIC_ED_KEY="${SPARKLE_PUBLIC_ED_KEY:-$(cat "$script_dir/../packaging/sparkle-public-key.txt")}"
feed_url="${SPARKLE_FEED_URL:-https://github.com/Emyrk/chronicle-sidekick/releases/latest/download/appcast.xml}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
app="$work/Chronicle Sidekick.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" "$app/Contents/Frameworks"
cp "$script_dir/../packaging/AppIcon.icns" "$app/Contents/Resources/AppIcon.icns"
ditto "$SPARKLE_DIR/Sparkle.framework" "$app/Contents/Frameworks/Sparkle.framework"
cp "$bin" "$app/Contents/MacOS/chronicle-sidekick"
chmod +x "$app/Contents/MacOS/chronicle-sidekick"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Chronicle Sidekick</string>
  <key>CFBundleIdentifier</key><string>com.chronicleclassic.sidekick</string>
  <key>CFBundleExecutable</key><string>chronicle-sidekick</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${version}</string>
  <key>CFBundleVersion</key><string>${version}</string>
  <key>LSMinimumSystemVersion</key><string>10.13</string>
  <key>LSUIElement</key><true/>
  <key>SUEnableAutomaticChecks</key><true/>
  <key>SUFeedURL</key><string>${feed_url}</string>
  <key>SUPublicEDKey</key><string>${SPARKLE_PUBLIC_ED_KEY}</string>
</dict>
</plist>
PLIST
# Ad-hoc signing provides local integrity for the bundle without requiring an
# Apple Developer ID. Sparkle's EdDSA signature authenticates downloaded updates.
codesign --force --deep --sign - "$app"
codesign --verify --deep --strict "$app"
out_abs="$(cd "$(dirname "$out")" && pwd)/$(basename "$out")"
(cd "$work" && ditto -c -k --sequesterRsrc --keepParent "Chronicle Sidekick.app" "$out_abs")
