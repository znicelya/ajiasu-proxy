#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
FETCH=$ROOT/runner/scripts/fetch-ajiasu.sh
TMP=$(mktemp -d)
HTTP_PID=
TLS_PID=
cleanup() {
  [ -z "$TLS_PID" ] || kill "$TLS_PID" 2>/dev/null || true
  [ -z "$HTTP_PID" ] || kill "$HTTP_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT HUP INT TERM

grep -F -- "--proto '=https,file'" "$FETCH" >/dev/null || {
  echo "fetch script is missing the restricted initial protocol list" >&2
  exit 1
}
grep -F -- "--proto-redir '=https'" "$FETCH" >/dev/null || {
  echo "fetch script is missing the HTTPS-only redirect protocol list" >&2
  exit 1
}

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

if AJIASU_URL="file://$TMP/ajiasu.tar.gz" AJIASU_SHA256=not-a-sha256 \
  AJIASU_SIZE="$GOOD_SIZE" \
  "$FETCH" amd64 "$OUT/malformed-sha"; then
  echo "fetch unexpectedly accepted malformed SHA metadata" >&2
  exit 1
fi
test ! -e "$OUT/malformed-sha/ajiasu"

for BAD_SIZE in invalid 0; do
  if AJIASU_URL="file://$TMP/ajiasu.tar.gz" AJIASU_SHA256="$GOOD_SHA" \
    AJIASU_SIZE="$BAD_SIZE" \
    "$FETCH" amd64 "$OUT/malformed-size-$BAD_SIZE"; then
    echo "fetch unexpectedly accepted invalid size metadata: $BAD_SIZE" >&2
    exit 1
  fi
  test ! -e "$OUT/malformed-size-$BAD_SIZE/ajiasu"
done

set +e
UNSUPPORTED_OUTPUT=$("$FETCH" riscv64 "$OUT/unsupported" 2>&1)
UNSUPPORTED_STATUS=$?
set -e
test "$UNSUPPORTED_STATUS" -eq 64
test "$UNSUPPORTED_OUTPUT" = "unsupported architecture: riscv64"
test ! -e "$OUT/unsupported/ajiasu"

if AJIASU_URL="file://$TMP/ajiasu.tar.gz" \
  AJIASU_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
  AJIASU_SIZE="$GOOD_SIZE" \
  "$FETCH" amd64 "$OUT/bad-sha"; then
  echo "fetch unexpectedly accepted a bad checksum" >&2
  exit 1
fi
test ! -e "$OUT/bad-sha/ajiasu"

if AJIASU_URL="file://$TMP/ajiasu.tar.gz" AJIASU_SHA256="$GOOD_SHA" \
  AJIASU_SIZE=1 \
  "$FETCH" amd64 "$OUT/bad-size"; then
  echo "fetch unexpectedly accepted a bad size" >&2
  exit 1
fi
test ! -e "$OUT/bad-size/ajiasu"

HTTP_PORT=18080
TLS_PORT=18443
{
  printf 'HTTP/1.1 200 OK\r\nContent-Length: %s\r\nConnection: close\r\n\r\n' "$GOOD_SIZE"
  cat "$TMP/ajiasu.tar.gz"
  sleep 10
} | nc -l -p "$HTTP_PORT" -s 127.0.0.1 >/dev/null 2>&1 &
HTTP_PID=$!

if AJIASU_URL="http://127.0.0.1:$HTTP_PORT/ajiasu.tar.gz" \
  AJIASU_SHA256="$GOOD_SHA" AJIASU_SIZE="$GOOD_SIZE" \
  "$FETCH" amd64 "$OUT/http"; then
  echo "fetch unexpectedly accepted a direct HTTP URL" >&2
  exit 1
fi
test ! -e "$OUT/http/ajiasu"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj /CN=localhost -addext subjectAltName=DNS:localhost \
  -keyout "$TMP/localhost.key" -out "$TMP/localhost.crt" >/dev/null 2>&1
{
  printf 'HTTP/1.1 302 Found\r\nLocation: http://127.0.0.1:%s/ajiasu.tar.gz\r\nContent-Length: 0\r\nConnection: close\r\n\r\n' "$HTTP_PORT"
  sleep 10
} | openssl s_server -quiet -accept "$TLS_PORT" \
  -cert "$TMP/localhost.crt" -key "$TMP/localhost.key" >/dev/null 2>&1 &
TLS_PID=$!
sleep 1

if CURL_CA_BUNDLE="$TMP/localhost.crt" \
  AJIASU_URL="https://localhost:$TLS_PORT/redirect" \
  AJIASU_SHA256="$GOOD_SHA" AJIASU_SIZE="$GOOD_SIZE" \
  "$FETCH" amd64 "$OUT/http-redirect"; then
  echo "fetch unexpectedly followed an HTTPS-to-HTTP redirect" >&2
  exit 1
fi
test ! -e "$OUT/http-redirect/ajiasu"

AJIASU_URL="file://$TMP/ajiasu.tar.gz" AJIASU_SHA256="$GOOD_SHA" \
  AJIASU_SIZE="$GOOD_SIZE" \
  "$FETCH" amd64 "$OUT/good"

test -x "$OUT/good/ajiasu"
test "$("$OUT/good/ajiasu")" = fake-ajiasu
