#!/bin/sh
set -eu

AJIASU_BIN=${AJIASU_BIN:-/usr/local/bin/ajiasu}
AJIASU_CONFIG=${AJIASU_CONFIG:-/run/ajiasu/ajiasu.conf}
AJIASU_RELAY_SOCKET=${AJIASU_RELAY_SOCKET:-/run/ajiasu-relay/runner.sock}

[ -f "$AJIASU_BIN" ] && [ -x "$AJIASU_BIN" ] || { echo 'ajiasu executable is unavailable' >&2; exit 66; }
test -f "$AJIASU_CONFIG" || { echo 'ajiasu config is unavailable' >&2; exit 66; }
case "$AJIASU_RELAY_SOCKET" in /run/ajiasu-relay/*.sock) ;; *) echo 'runner relay socket path is invalid' >&2; exit 78 ;; esac

MODE=$(stat -L -c '%a' -- "$AJIASU_CONFIG")
case "$MODE" in
  400|600) ;;
  *) echo "ajiasu config permissions must be 0400 or 0600, got $MODE" >&2; exit 77 ;;
esac

export AJIASU_CONFIG
exec "$AJIASU_BIN" "$@"
