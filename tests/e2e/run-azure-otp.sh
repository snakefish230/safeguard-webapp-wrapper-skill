#!/usr/bin/env bash
# E2E: drive the real azure wrapper against a mock Entra sign-in that reproduces
# the two OTP faults: "sign in another way" present on the code page, and a
# first code that is rejected. Asserts no detour and a successful retry.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"; T="$(mktemp -d)"; SRV=
trap '[ -n "$SRV" ] && kill $SRV 2>/dev/null; rm -rf "$T"' EXIT
if [ -z "${CHROME:-}" ]; then
  for c in google-chrome chromium chromium-browser; do
    if command -v "$c" >/dev/null 2>&1; then CHROME="$(command -v "$c")"; break; fi
  done
fi
[ -n "${CHROME:-}" ] && [ -x "$CHROME" ] || { echo "no Chrome/Chromium found; set CHROME=<path>"; exit 1; }
printf '#!/bin/sh\nexec "%s" --no-sandbox "$@"\n' "$CHROME" > "$T/google-chrome"; chmod +x "$T/google-chrome"
export PATH="$T:$PATH"
(cd "$ROOT/wrapper" && go build -o "$T/azure" ./sites/azure)
PORT=18766; python3 "$ROOT/tests/e2e/entra_otp.py" $PORT "$T/state.json" & SRV=$!; sleep 1
NOW=$(date +%s)
# Code A is current, code B starts next period; A is rejected by the mock.
STDIN=$(printf '{"username":"alice@contoso.onmicrosoft.com","password":"S3cr3t!pa ss","asset":"127.0.0.1:%s","Target.TotpCodes":[{"Code":"000001","UnixTime":%s,"Period":30},{"Code":"123456","UnixTime":%s,"Period":30}],"cli_args":"-url http://127.0.0.1:%s/ -no-wait -step-timeout 25s -debug"}' $PORT $((NOW-2)) $((NOW+28)) $PORT)
: "${TEST_DISPLAY:=}"
if [ -z "$TEST_DISPLAY" ] && command -v Xvfb >/dev/null 2>&1; then
  Xvfb :98 -screen 0 1280x900x24 >/dev/null 2>&1 & XV=$!; sleep 1; TEST_DISPLAY=:98
fi
[ -n "$TEST_DISPLAY" ] || TEST_DISPLAY="${DISPLAY:-}"
[ -n "$TEST_DISPLAY" ] || { echo "no X display: install Xvfb or set TEST_DISPLAY/DISPLAY"; exit 1; }
echo "using display: $TEST_DISPLAY"
echo "$STDIN" | DISPLAY="$TEST_DISPLAY" APPDATA="$T" "$T/azure" || true
sleep 1
[ -n "${XV:-}" ] && kill $XV 2>/dev/null || true
echo "--- state"; cat "$T/state.json"; echo
python3 - "$T/state.json" <<'EOF'
import json,sys
s=json.load(open(sys.argv[1]))
assert s["detour"] is False, "FAIL: took the 'sign in another way' detour with the OTP box already on screen"
print("PASS: no detour when the verification-code page is already showing")
assert len(s["codes"])==2, f"FAIL: expected 2 OTP attempts, got {s['codes']}"
assert s["codes"][0]!=s["codes"][1], f"FAIL: retried with the same code {s['codes']}"
print(f"PASS: rejected code retried with a different one ({s['codes'][0]} then {s['codes'][1]})")
assert s["accepted"], "FAIL: OTP never accepted"
assert s["user"]=="alice@contoso.onmicrosoft.com" and s["pass"]=="S3cr3t!pa ss"
print("PASS: credentials injected and sign-in completed")
EOF
L=$(cat "$T"/OneIdentity/*/azure_*.log)
grep -q 'S3cr3t' <<<"$L" && { echo "FAIL: password in log"; exit 1; }
grep -q '123456\|000001' <<<"$L" && { echo "FAIL: OTP code in log"; exit 1; }
echo "PASS: neither password nor OTP code in log"
