#!/bin/sh
set -eu

AJIASU_BIN=${AJIASU_BIN:?AJIASU_BIN is required}
CONFIG=${AJIASU_CONFIG:?AJIASU_CONFIG is required}

help=$("$AJIASU_BIN" -h 2>&1 || true)
printf '%s' "$help" | grep -E 'login|list|connect'

login=$("$AJIASU_BIN" login 2>&1)
printf '%s' "$login" | grep -F 'Login Result: OK'

nodes=$("$AJIASU_BIN" list 2>&1)
printf '%s' "$nodes" | grep -E '(^|[[:space:]])vvn-'

test -s "$CONFIG"
