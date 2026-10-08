#!/usr/bin/env bash
# Installs the Delve Atlas systemd user units and starts them.
#
#   deploy/install.sh              install/refresh the units, enable and start everything
#   deploy/install.sh restart      rebuild the binaries and restart the long-running services
#
# Units run as your user (no root). `loginctl enable-linger $USER` makes them start at boot
# without a login session. Secrets stay in ~/.config/delve-atlas/env (KEY=value lines).
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
dest=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
services=(delve-ingest delve-embed delve-resolve delve-atlas-web)

if [ "${1:-}" = "restart" ]; then
  (cd "$repo" && make build)
  systemctl --user restart "${services[@]/%/.service}"
  exit 0
fi

(cd "$repo" && make build)
mkdir -p "$dest"
for f in "$repo"/deploy/systemd/*; do
  sed "s|@REPO@|$repo|g" "$f" > "$dest/$(basename "$f")"
done
systemctl --user daemon-reload
systemctl --user enable --now "${services[@]/%/.service}" delve-rebuild.timer
loginctl show-user "$USER" 2>/dev/null | grep -q '^Linger=yes' || \
  echo "note: run 'loginctl enable-linger $USER' so these start at boot" >&2
systemctl --user list-units 'delve-*' --no-pager
