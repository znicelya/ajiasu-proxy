#!/bin/sh
set -eu

AJIASU_BIN=${AJIASU_BIN:?AJIASU_BIN is required}
CONFIG=${AJIASU_CONFIG:?AJIASU_CONFIG is required}

[ -f "$CONFIG" ] && [ -r "$CONFIG" ] && [ -s "$CONFIG" ] || {
  echo 'AJIASU_CONFIG must be a readable, nonempty regular file' >&2
  exit 66
}

help=$("$AJIASU_BIN" -h 2>&1 || true)
printf '%s' "$help" | grep -Eq '(^|[^[:alnum:]_])login([^[:alnum:]_]|$)'
printf '%s' "$help" | grep -Eq '(^|[^[:alnum:]_])list([^[:alnum:]_]|$)'
printf '%s' "$help" | grep -Eq '(^|[^[:alnum:]_])connect([^[:alnum:]_]|$)'

login=$("$AJIASU_BIN" login 2>&1)
printf '%s\n' "$login" | grep -Fxq 'Login Result: OK'

nodes=$("$AJIASU_BIN" list 2>&1)
printf '%s\n' "$nodes" | grep -Eq '^vvn-[[:alnum:]-]+[[:space:]]+ok([[:space:]]|$)'
