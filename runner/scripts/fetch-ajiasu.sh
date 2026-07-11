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
    ;;
  arm64)
    OFFICIAL_URL=$AJIASU_ARM64_URL
    OFFICIAL_SHA256=$AJIASU_ARM64_SHA256
    ;;
  *)
    echo "unsupported architecture: $ARCH" >&2
    exit 64
    ;;
esac

URL=${AJIASU_URL:-$OFFICIAL_URL}
SHA256=${AJIASU_SHA256:-$OFFICIAL_SHA256}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

mkdir -p "$OUT"
curl --fail --location --proto '=https,file' --tlsv1.2 \
  --output "$TMP/ajiasu.tar.gz" "$URL"
printf '%s  %s\n' "$SHA256" "$TMP/ajiasu.tar.gz" | sha256sum -c -
tar -xzf "$TMP/ajiasu.tar.gz" -C "$TMP" ajiasu
install -m 0755 "$TMP/ajiasu" "$OUT/ajiasu"
