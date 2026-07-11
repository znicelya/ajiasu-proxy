#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

SOURCE=$TMP/source
OUT=$TMP/out
mkdir -p "$SOURCE" "$OUT"

cat >"$SOURCE/ajiasu" <<'EOF'
#!/bin/sh
printf '%s\n' fake-ajiasu
EOF
chmod +x "$SOURCE/ajiasu"
tar -C "$SOURCE" -czf "$TMP/ajiasu.tar.gz" ajiasu
GOOD_SHA=$(sha256sum "$TMP/ajiasu.tar.gz" | awk '{print $1}')
GOOD_SIZE=$(wc -c <"$TMP/ajiasu.tar.gz" | tr -d '[:space:]')

if AJIASU_URL="file://$TMP/ajiasu.tar.gz" \
  AJIASU_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
  AJIASU_SIZE="$GOOD_SIZE" \
  "$ROOT/runner/scripts/fetch-ajiasu.sh" amd64 "$OUT/bad-sha"; then
  echo "fetch unexpectedly accepted a bad checksum" >&2
  exit 1
fi
test ! -e "$OUT/bad-sha/ajiasu"

if AJIASU_URL="file://$TMP/ajiasu.tar.gz" AJIASU_SHA256="$GOOD_SHA" \
  AJIASU_SIZE=1 \
  "$ROOT/runner/scripts/fetch-ajiasu.sh" amd64 "$OUT/bad-size"; then
  echo "fetch unexpectedly accepted a bad size" >&2
  exit 1
fi
test ! -e "$OUT/bad-size/ajiasu"

AJIASU_URL="file://$TMP/ajiasu.tar.gz" AJIASU_SHA256="$GOOD_SHA" \
  AJIASU_SIZE="$GOOD_SIZE" \
  "$ROOT/runner/scripts/fetch-ajiasu.sh" amd64 "$OUT/good"

test -x "$OUT/good/ajiasu"
test "$("$OUT/good/ajiasu")" = fake-ajiasu
