#!/usr/bin/env bash
# Download the pinned Sparkle framework and tools used by macOS builds.
set -euo pipefail
version=2.9.6
sha256=52bf9e88cdd972fc0c81501377a880e90d47031bd8ca5462488f843e2609e192
root="${SPARKLE_CACHE_DIR:-$PWD/.cache/sparkle-$version}"
archive="$root/Sparkle-$version.tar.xz"
dist="$root/dist"
mkdir -p "$root"
if [[ ! -f "$archive" ]]; then
  curl -fsSL "https://github.com/sparkle-project/Sparkle/releases/download/$version/Sparkle-$version.tar.xz" -o "$archive.tmp"
  mv "$archive.tmp" "$archive"
fi
printf '%s  %s\n' "$sha256" "$archive" | shasum -a 256 -c - >/dev/null
if [[ ! -d "$dist/Sparkle.framework" ]]; then
  rm -rf "$dist"
  mkdir -p "$dist"
  tar -xJf "$archive" -C "$dist"
fi
printf '%s\n' "$dist"
