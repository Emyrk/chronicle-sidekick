#!/usr/bin/env bash
# Put the packaged Chronicle Sidekick.app in a drag-to-Applications disk image.
# Usage: macos-dmg.sh <app.zip> <out.dmg>
set -euo pipefail

archive="$1"
out="$2"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

archive_abs="$(cd "$(dirname "$archive")" && pwd)/$(basename "$archive")"
out_abs="$(cd "$(dirname "$out")" && pwd)/$(basename "$out")"

mkdir -p "$work/unpacked" "$work/image"
ditto -x -k "$archive_abs" "$work/unpacked"
app="$work/unpacked/Chronicle Sidekick.app"
test -d "$app"

mv "$app" "$work/image/Chronicle Sidekick.app"
ln -s /Applications "$work/image/Applications"
hdiutil create \
  -volname "Chronicle Sidekick" \
  -srcfolder "$work/image" \
  -ov \
  -format UDZO \
  "$out_abs"
hdiutil verify "$out_abs"
