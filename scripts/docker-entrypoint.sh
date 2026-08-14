#!/bin/sh
set -eu

# pcscd daemonizes after startup. Keep failure non-fatal so modem-only
# deployments remain usable and the UI can report a reader diagnostic.
if [ "$(id -u)" = "0" ] && command -v pcscd >/dev/null 2>&1; then
  mkdir -p /run/pcscd
  pcscd || echo "warning: pcscd failed to start; USB SIM readers may be unavailable" >&2
fi

exec /opt/vocat/bin/vocat "$@"
