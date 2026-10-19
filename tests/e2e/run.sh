#!/usr/bin/env bash
# E2E: build generic wrapper for linux, feed Launcher-style stdin JSON, drive real Chrome
# under Xvfb against the mock site, assert creds + fresh TOTP arrived and password not logged.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"; T="$(mktemp -d)"; SRV=
trap '[ -n "$SRV" ] && kill $SRV 2>/dev/null; rm -rf "$T"' EXIT
[ -n "${GO_BIN_DIR:-}" ] && export PATH="$GO_BIN_DIR:$PATH"
# Browser: explicit $CHROME, else the first one on PATH.
if [ -z "${CHROME:-}" ]; then
  for c in google-chrome chromium chromium-browser; do
    if command -v "$c" >/dev/null 2>&1; then CHROME="$(command -v "$c")"; break; fi
  done
fi
[ -n "${CHROME:-}" ] && [ -x "$CHROME" ] || { echo "no Chrome/Chromium found; set CHROME=<path>"; exit 1; }
echo "using browser: $CHROME"
printf '#!/bin/sh\nexec "%s" --no-sandbox "$@"\n' "$CHROME" > "$T/google-chrome"; chmod +x "$T/google-chrome"
export PATH="$T:$PATH"
(cd "$ROOT/wrapper" && go build -o "$T/generic" ./sites/generic)
PORT=18765; python3 "$ROOT/tests/e2e/mocksite.py" $PORT "$T/sub.jsonl" & SRV=$!; sleep 1
NOW=$(date +%s)
STDIN=$(printf '{"username":"alice","password":"S3cr3t!pa ss","asset":"127.0.0.1:%s","Target.TotpCodes":[{"Code":"000001","UnixTime":%s,"Period":30},{"Code":"123456","UnixTime":%s,"Period":30}],"cli_args":"-url http://{asset}/login -user-sel #username -next-sel #next -pass-sel #password -submit-sel #signin -otp-sel #otp -otp-submit-sel #verify -success-sel #welcome -no-wait -step-timeout 20s -debug"}' $PORT $((NOW-29)) $((NOW+1)))
: "${TEST_DISPLAY:=}"
if [ -z "$TEST_DISPLAY" ] && command -v Xvfb >/dev/null 2>&1; then
  Xvfb :97 -screen 0 1280x900x24 >/dev/null 2>&1 & XV=$!; sleep 1
  TEST_DISPLAY=:97
fi
[ -n "$TEST_DISPLAY" ] || TEST_DISPLAY="${DISPLAY:-}"
[ -n "$TEST_DISPLAY" ] || { echo "no X display: install Xvfb or set TEST_DISPLAY/DISPLAY"; exit 1; }
echo "using display: $TEST_DISPLAY"
echo "$STDIN" | DISPLAY="$TEST_DISPLAY" APPDATA="$T" "$T/generic"
[ -n "${XV:-}" ] && kill $XV 2>/dev/null || true
sleep 1
echo "--- submission"; cat "$T/sub.jsonl"
python3 - "$T/sub.jsonl" <<'EOF'
import json,sys; s=json.loads(open(sys.argv[1]).read().splitlines()[-1])
assert s=={"u":"alice","p":"S3cr3t!pa ss","o":"123456"}, s
print("PASS: username, password, fresh TOTP injected")
EOF
L=$(cat "$T"/OneIdentity/*/generic_*.log); echo "--- log tail"; echo "$L" | tail -4
if grep -q 'S3cr3t' <<<"$L"; then echo "FAIL: password in log"; exit 1; else echo "PASS: password not in log"; fi
