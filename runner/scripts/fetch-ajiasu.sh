#!/bin/sh
set -eu

ARCH=${1:?target architecture is required}
OUT=${2:?output directory is required}

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
. "$ROOT/runner/artifacts/ajiasu-4.2.3.0.env"

case "$ARCH" in
  amd64)
    OFFICIAL_URL=$AJIASU_AMD64_URL
    OFFICIAL_SHA256=$AJIASU_AMD64_SHA256
    OFFICIAL_SIZE=$AJIASU_AMD64_SIZE
    ;;
  arm64)
    OFFICIAL_URL=$AJIASU_ARM64_URL
    OFFICIAL_SHA256=$AJIASU_ARM64_SHA256
    OFFICIAL_SIZE=$AJIASU_ARM64_SIZE
    ;;
  *)
    echo "unsupported architecture: $ARCH" >&2
    exit 64
    ;;
esac

URL=${AJIASU_URL:-$OFFICIAL_URL}
SHA256=${AJIASU_SHA256:-$OFFICIAL_SHA256}
SIZE=${AJIASU_SIZE:-$OFFICIAL_SIZE}

case "$SHA256" in
  *[!0-9A-Fa-f]*)
    echo "invalid SHA-256 metadata: expected 64 hexadecimal characters" >&2
    exit 1
    ;;
esac
if [ "${#SHA256}" -ne 64 ]; then
  echo "invalid SHA-256 metadata: expected 64 hexadecimal characters" >&2
  exit 1
fi
case "$SIZE" in
  '' | *[!0-9]*)
    echo "invalid size metadata: expected a positive decimal integer" >&2
    exit 1
    ;;
esac
if [ "$SIZE" -le 0 ]; then
  echo "invalid size metadata: expected a positive decimal integer" >&2
  exit 1
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

mkdir -p "$OUT"
curl --fail --location --proto '=https,file' --proto-redir '=https' --tlsv1.2 \
  --max-filesize "$SIZE" \
  --output "$TMP/ajiasu.tar.gz" "$URL"
ACTUAL_SIZE=$(wc -c <"$TMP/ajiasu.tar.gz" | tr -d '[:space:]')
if [ "$ACTUAL_SIZE" != "$SIZE" ]; then
  echo "artifact size mismatch: expected $SIZE bytes, got $ACTUAL_SIZE" >&2
  exit 1
fi
printf '%s  %s\n' "$SHA256" "$TMP/ajiasu.tar.gz" | sha256sum -c -
tar -xzf "$TMP/ajiasu.tar.gz" -C "$TMP" ajiasu
install -m 0755 "$TMP/ajiasu" "$OUT/ajiasu"
