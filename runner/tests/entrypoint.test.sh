#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

BIN=$TMP/bin/ajiasu
ARGUMENT_PROBE=$TMP/bin/argument-probe
CONFIG=$TMP/ajiasu.conf
mkdir -p "$TMP/bin"
install -m 0755 "$ROOT/runner/testdata/fake-ajiasu.sh" "$BIN"
cat >"$ARGUMENT_PROBE" <<'EOF'
#!/bin/sh
set -eu

printf 'argc=%s\n' "$#"
INDEX=1
for ARG
do
  printf 'arg[%s]=<%s>\n' "$INDEX" "$ARG"
  INDEX=$((INDEX + 1))
done
EOF
chmod 0755 "$ARGUMENT_PROBE"
printf 'user example\npass secret\n' >"$CONFIG"
chmod 0600 "$CONFIG"

OUTPUT=$(AJIASU_BIN="$BIN" AJIASU_CONFIG="$CONFIG" \
  "$ROOT/runner/bin/runner-entrypoint.sh" login)
printf '%s\n' "$OUTPUT" | grep -F 'Login Result: OK' >/dev/null

chmod 0644 "$CONFIG"
if AJIASU_BIN="$BIN" AJIASU_CONFIG="$CONFIG" \
  "$ROOT/runner/bin/runner-entrypoint.sh" login >/dev/null 2>&1; then
  echo 'entrypoint unexpectedly accepted config mode 0644' >&2
  exit 1
fi

chmod 0400 "$CONFIG"
READ_ONLY_OUTPUT=$(AJIASU_BIN="$BIN" AJIASU_CONFIG="$CONFIG" \
  "$ROOT/runner/bin/runner-entrypoint.sh" login)
printf '%s\n' "$READ_ONLY_OUTPUT" | grep -F 'Login Result: OK' >/dev/null

LIST_OUTPUT=$(AJIASU_BIN="$BIN" AJIASU_CONFIG="$CONFIG" \
  "$ROOT/runner/bin/runner-entrypoint.sh" list)
printf '%s\n' "$LIST_OUTPUT" | grep -F 'Command: list' >/dev/null
printf '%s\n' "$LIST_OUTPUT" | grep -F 'vvn-test-1 ok Test Node #1' >/dev/null

ARGUMENT_OUTPUT=$(AJIASU_BIN="$ARGUMENT_PROBE" AJIASU_CONFIG="$CONFIG" \
  "$ROOT/runner/bin/runner-entrypoint.sh" first 'two words' third)
EXPECTED_ARGUMENT_OUTPUT='argc=3
arg[1]=<first>
arg[2]=<two words>
arg[3]=<third>'
if [ "$ARGUMENT_OUTPUT" != "$EXPECTED_ARGUMENT_OUTPUT" ]; then
  printf 'unexpected argument probe output:\n%s\n' "$ARGUMENT_OUTPUT" >&2
  exit 1
fi

rm "$CONFIG"
set +e
MISSING_CONFIG_OUTPUT=$(AJIASU_BIN="$BIN" AJIASU_CONFIG="$CONFIG" \
  "$ROOT/runner/bin/runner-entrypoint.sh" login 2>&1)
MISSING_CONFIG_STATUS=$?
set -e
if [ "$MISSING_CONFIG_STATUS" -ne 66 ]; then
  echo "missing config exited $MISSING_CONFIG_STATUS instead of 66" >&2
  exit 1
fi
if [ "$MISSING_CONFIG_OUTPUT" != 'ajiasu config is unavailable' ]; then
  echo "unexpected missing config message: $MISSING_CONFIG_OUTPUT" >&2
  exit 1
fi

printf 'user example\npass secret\n' >"$CONFIG"
chmod 0600 "$CONFIG"
chmod 0644 "$BIN"
set +e
NON_EXECUTABLE_OUTPUT=$(AJIASU_BIN="$BIN" AJIASU_CONFIG="$CONFIG" \
  "$ROOT/runner/bin/runner-entrypoint.sh" login 2>&1)
NON_EXECUTABLE_STATUS=$?
set -e
if [ "$NON_EXECUTABLE_STATUS" -ne 66 ]; then
  echo "non-executable binary exited $NON_EXECUTABLE_STATUS instead of 66" >&2
  exit 1
fi
if [ "$NON_EXECUTABLE_OUTPUT" != 'ajiasu executable is unavailable' ]; then
  echo "unexpected non-executable binary message: $NON_EXECUTABLE_OUTPUT" >&2
  exit 1
fi
