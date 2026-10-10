#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Regenerate every myriarch logo asset from the construction in build.py and brandlib.py.
#
# Usage: assets/brand/source/make_assets.sh [<out-dir>]
#
# <out-dir> defaults to assets/brand. The tools live in BRAND_TOOLS_DIR (default: .cache/brand in the
# repository root) and are set up on the first run, so nothing is installed system-wide:
#   venv/   Python venv with the packages pinned in requirements.txt (recreated when the pins change)
#   node/   npm ci of package.json and package-lock.json (@resvg/resvg-js, svgo)
#   fonts/  Inter TTFs from the pinned release archive, checked against its SHA-256
#   work/   unoptimized SVGs, raster sources, geometry.log and contact-sheet.png (review only)
# Needs Python 3.10 or newer (Pillow 12), node with npm, curl and unzip. The venv is created by python3;
# BRAND_PYTHON selects another interpreter when the python3 in PATH is older (the macOS Command Line Tools
# ship 3.9), for example BRAND_PYTHON=python3.12.
set -euo pipefail

INTER_VERSION=4.1
INTER_SHA256=9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e
INTER_URL="https://github.com/rsms/inter/releases/download/v${INTER_VERSION}/Inter-${INTER_VERSION}.zip"

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SRC/../../.." && pwd)"
OUT="${1:-$SRC/..}"
mkdir -p "$OUT"
B="$(cd "$OUT" && pwd)"
T="${BRAND_TOOLS_DIR:-$ROOT/.cache/brand}"
mkdir -p "$T"
T="$(cd "$T" && pwd)"
W="$T/work"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

if [ ! -x "$T/venv/bin/python" ] || ! cmp -s "$SRC/requirements.txt" "$T/venv/requirements.txt"; then
  BRAND_PYTHON="${BRAND_PYTHON:-python3}"
  # The pinned Pillow needs Python 3.10 or newer; an older interpreter only fails later with "No matching distribution".
  "$BRAND_PYTHON" -c 'import sys; sys.exit(sys.version_info < (3, 10))' >/dev/null 2>&1 || {
    echo "make_assets.sh: $BRAND_PYTHON is missing or older than Python 3.10 (needed by the packages in requirements.txt)." >&2
    echo "Set BRAND_PYTHON to another interpreter, for example BRAND_PYTHON=python3.12." >&2
    exit 1
  }
  echo "== set up Python venv"
  # --clear starts from an empty venv: an existing one may come from an interpreter that is too old, and it must not keep
  # packages that requirements.txt no longer pins.
  "$BRAND_PYTHON" -m venv --clear "$T/venv"
  "$T/venv/bin/python" -m pip install --quiet --disable-pip-version-check -r "$SRC/requirements.txt"
  cp "$SRC/requirements.txt" "$T/venv/requirements.txt"
fi

if [ ! -d "$T/node/node_modules" ] || ! cmp -s "$SRC/package-lock.json" "$T/node/package-lock.json"; then
  echo "== install Node packages"
  mkdir -p "$T/node"
  cp "$SRC/package.json" "$SRC/package-lock.json" "$T/node/"
  (cd "$T/node" && npm ci --ignore-scripts --no-audit --no-fund)
fi

if [ ! -f "$T/fonts/Inter-SemiBold.ttf" ] || [ ! -f "$T/fonts/Inter-Regular.ttf" ]; then
  echo "== download Inter ${INTER_VERSION}"
  mkdir -p "$T/dl" "$T/fonts"
  zip="$T/dl/Inter-${INTER_VERSION}.zip"
  curl -fsSL --retry 3 -o "$zip" "$INTER_URL"
  got="$(sha256 "$zip")"
  if [ "$got" != "$INTER_SHA256" ]; then
    rm -f "$zip"
    echo "Inter-${INTER_VERSION}.zip: SHA-256 mismatch: got ${got}, want ${INTER_SHA256}" >&2
    exit 1
  fi
  unzip -o -j -q "$zip" extras/ttf/Inter-SemiBold.ttf extras/ttf/Inter-Regular.ttf LICENSE.txt -d "$T/fonts"
fi

PY="$T/venv/bin/python"
export BRAND_NODE_DIR="$T/node"
render() { node "$SRC/render.mjs" "$@"; }

rm -rf "$W/raw"
mkdir -p "$W/raw"

echo "== build SVG sources"
"$PY" -I "$SRC/build.py" "$T/fonts" "$W/raw" >"$W/geometry.log"

echo "== optimize (svgo) into $B"
(cd "$W/raw" && node "$SRC/optimize.mjs" "$W/raw" "$B" ./*.svg)

echo "== validate"
"$PY" -I "$SRC/validate.py" "$B"/*.svg

echo "== rasterize (resvg)"
render "$B/favicon.svg" "$B/favicon-16.png" --width 16
render "$W/raw/raster-src/favicon-32.svg" "$B/favicon-32.png" --width 32
render "$W/raw/raster-src/favicon-48.svg" "$W/favicon-48.png" --width 48
render "$W/raw/raster-src/apple-touch-icon.svg" "$B/apple-touch-icon.png" --width 180
render "$W/raw/raster-src/icon-192.svg" "$B/icon-192.png" --width 192
render "$W/raw/raster-src/icon-512.svg" "$B/icon-512.png" --width 512
render "$B/social-preview.svg" "$B/social-preview.png" --width 1280
render "$B/og-image.svg" "$B/og-image.png" --width 1200

echo "== finish PNGs, build favicon.ico"
"$PY" -I "$SRC/finish_rasters.py" "$B" "$W/favicon-48.png"

echo "== contact sheet (review only, not committed)"
"$PY" -I "$SRC/contact_sheet.py" "$B" "$W/favicon-48.png" "$W/contact-sheet.svg"
render "$W/contact-sheet.svg" "$W/contact-sheet.png" --font "$T/fonts/Inter-Regular.ttf"
