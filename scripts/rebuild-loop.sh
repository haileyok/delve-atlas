#!/usr/bin/env bash
# Keeps the atlas fresh: every INTERVAL seconds (default 3h) embed new posts, resolve new
# accounts, rebuild the snapshot, and prune old snapshots. The server picks up the newest one
# on its next request, so nothing needs restarting.
#
# The schedule is anchored to the newest snapshot, not to when this script started: on startup it
# waits only for whatever is left of INTERVAL since the last build, so restarting the service
# neither rebuilds straight away nor pushes the next build further out.
#
#   scripts/rebuild-loop.sh            # loop forever
#   scripts/rebuild-loop.sh once       # a single pass, now
set -euo pipefail
cd "$(dirname "$0")/.."

ENV_FILE=${ENV_FILE:-$HOME/.config/delve-atlas/env}
[ -f "$ENV_FILE" ] && { set -a; . "$ENV_FILE"; set +a; }
INTERVAL=${INTERVAL:-10800}
KEEP=${KEEP_SNAPSHOTS:-16}
DAYS=${WINDOW_DAYS:-7}

pass() {
  echo "$(date -u +%FT%TZ) rebuild: resolving accounts"
  ./bin/resolve -db data/delve.db
  echo "$(date -u +%FT%TZ) rebuild: embedding"
  ./bin/embed -db data/delve.db -days "$DAYS"
  echo "$(date -u +%FT%TZ) rebuild: building atlas"
  (cd pipeline && uv run python build_atlas.py --db ../data/delve.db --out ../data/atlas --days "$DAYS" --workers 16 --prune-unit-cache 2> >(grep -v -i -E 'warn|n_jobs' >&2))
  # The link-preview card is drawn from the newest map by the running server's /og/card.html in
  # headless Chromium. If that fails the site keeps showing the previous card.
  if [ -f tools/og.mjs ] && command -v node >/dev/null; then
    echo "$(date -u +%FT%TZ) rebuild: rendering the link-preview card"
    node tools/og.mjs --base "${OG_BASE:-http://127.0.0.1:8088}" --out data/atlas/latest/og.jpg \
      || echo "$(date -u +%FT%TZ) rebuild: card render failed; the previous card stays" >&2
  fi
  # keep the newest $KEEP snapshots (never the one `latest` points at)
  latest=$(readlink data/atlas/latest || true)
  ls -1d data/atlas/2*Z 2>/dev/null | sort | head -n -"$KEEP" | while read -r d; do
    [ "$(basename "$d")" = "$latest" ] || rm -rf "$d"
  done
  echo "$(date -u +%FT%TZ) rebuild: done"
}

# Seconds until the next build is due, given the age of the newest snapshot (0 if none).
wait_for_due() {
  local f=data/atlas/latest/atlas.json
  [ -f "$f" ] || { echo 0; return; }
  local age=$(( $(date +%s) - $(stat -L -c %Y "$f") ))
  local left=$(( INTERVAL - age ))
  [ "$left" -gt 0 ] && echo "$left" || echo 0
}

if [ "${1:-}" = "once" ]; then pass; exit 0; fi

first=$(wait_for_due)
echo "$(date -u +%FT%TZ) rebuild: interval ${INTERVAL}s; next build in ${first}s"
sleep "$first"
while true; do
  pass || echo "$(date -u +%FT%TZ) rebuild failed; will retry next interval" >&2
  sleep "$INTERVAL"
done
