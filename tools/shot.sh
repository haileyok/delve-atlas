#!/usr/bin/env bash
# Screenshot the running atlas in headless Chromium.
#   tools/shot.sh OUT.png [URL-path-with-hash] [WIDTHxHEIGHT] [wait-ms]
# Needs CHROMIUM (path to the binary); defaults to the nix-built one if present.
set -euo pipefail
out=${1:?output png}
path=${2:-/}
size=${3:-1600x900}
wait=${4:-9000}
chromium=${CHROMIUM:-$(ls -d /nix/store/*-chromium-*/bin/chromium 2>/dev/null | head -1)}
[ -x "$chromium" ] || { echo "set CHROMIUM to a chromium binary" >&2; exit 1; }
profile=$(mktemp -d)
trap 'rm -rf "$profile"' EXIT
"$chromium" --headless=new --no-sandbox --disable-gpu-sandbox --user-data-dir="$profile" \
  --use-gl=angle --use-angle=swiftshader --enable-unsafe-swiftshader --ignore-gpu-blocklist \
  --window-size="${size/x/,}" --hide-scrollbars --enable-logging=stderr --v=0 \
  --virtual-time-budget="$wait" --screenshot="$out" "http://127.0.0.1:${PORT:-8088}${path}" 2> >(grep -i -E "console|error|CONSOLE" | grep -v -E "dbus|gpu_|viz|vaapi|angle_platform|ContextResult|sandbox" | head -20 >&2) || true
echo "wrote $out"
