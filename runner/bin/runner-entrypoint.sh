#!/bin/sh
set -eu

AJIASU_BIN=${AJIASU_BIN:-/usr/local/bin/ajiasu}
AJIASU_CONFIG=${AJIASU_CONFIG:-/run/ajiasu/ajiasu.conf}

test -x "$AJIASU_BIN" || { echo 'ajiasu executable is unavailable' >&2; exit 66; }
test -f "$AJIASU_CONFIG" || { echo 'ajiasu config is unavailable' >&2; exit 66; }

MODE=$(stat -c '%a' "$AJIASU_CONFIG")
case "$MODE" in
  400|600) ;;
  *) echo "ajiasu config permissions must be 0400 or 0600, got $MODE" >&2; exit 77 ;;
esac

export AJIASU_CONFIG
exec "$AJIASU_BIN" "$@"
