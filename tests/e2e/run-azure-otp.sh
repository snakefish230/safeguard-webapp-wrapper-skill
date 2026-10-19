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
: "${TEST_DISPLAY:=}"
if [ -z "$TEST_DISPLAY" ] && command -v Xvfb >/dev/null 2>&1; then
  Xvfb :98 -screen 0 1280x900x24 >/dev/null 2>&1 & XV=$!; sleep 1; TEST_DISPLAY=:98
fi
[ -n "$TEST_DISPLAY" ] || TEST_DISPLAY="${DISPLAY:-}"
[ -n "$TEST_DISPLAY" ] || { echo "no X display: install Xvfb or set TEST_DISPLAY/DISPLAY"; exit 1; }
echo "using display: $TEST_DISPLAY"

# $1 = url query, $2 = expected detour (true/false), $3 = human label
run_case() {
  rm -f "$T/state.json" "$T"/OneIdentity/*/azure_*.log 2>/dev/null || true
  curl -s "http://127.0.0.1:$PORT/reset" >/dev/null
  NOW=$(date +%s)
  # Code A is current, code B starts next period; A is rejected by the mock.
  STDIN=$(printf '{"username":"alice@contoso.onmicrosoft.com","password":"S3cr3t!pa ss","asset":"127.0.0.1:%s","Target.TotpCodes":[{"Code":"000001","UnixTime":%s,"Period":30},{"Code":"123456","UnixTime":%s,"Period":30}],"cli_args":"-url http://127.0.0.1:%s/%s -no-wait -step-timeout 25s -debug"}' \
    $PORT $((NOW-2)) $((NOW+28)) $PORT "$1")
  START=$(date +%s)
  echo "$STDIN" | DISPLAY="$TEST_DISPLAY" APPDATA="$T" "$T/azure" >"$T/out.txt" 2>&1 || true
  ELAPSED=$(( $(date +%s) - START ))
  echo "--- $3: ${ELAPSED}s"; cat "$T/out.txt"; cat "$T/state.json"; echo
  # The site accepting the code and the wrapper believing it did are separate
  # facts; assert both, or a wrapper that misreads success looks like a pass.
  if grep -q "Automatic sign-in failed" "$T/out.txt"; then
    echo "FAIL [$3]: site accepted the code but the wrapper reported failure"; exit 1; fi
  EXPECT_DETOUR="$2" ELAPSED="$ELAPSED" LABEL="$3" python3 - "$T/state.json" <<'EOF'
import json,os,sys
s=json.load(open(sys.argv[1])); lab=os.environ["LABEL"]
want = os.environ["EXPECT_DETOUR"]=="true"
assert s["detour"] is want, f"FAIL [{lab}]: detour={s['detour']}, expected {want}"
print(f"PASS [{lab}]: detour taken only when needed ({s['detour']})")
assert len(s["codes"])==2 and s["codes"][0]!=s["codes"][1], f"FAIL [{lab}]: codes {s['codes']}"
assert s["accepted"], f"FAIL [{lab}]: OTP never accepted"
print(f"PASS [{lab}]: rejected code retried with a different one {s['codes']}")
assert s["user"]=="alice@contoso.onmicrosoft.com" and s["password"]=="S3cr3t!pa ss"
# One submission only. A click that silently misses plus an Enter fallback must
# still send the password exactly once.
assert s["submits"]==1, f"FAIL [{lab}]: password submitted {s['submits']} times, expected 1"
print(f"PASS [{lab}]: password submitted exactly once")
# The mock renders instantly, so anything near a fixed probe timeout is a
# regression back to waiting on one selector before testing the other.
assert int(os.environ["ELAPSED"]) < 20, f"FAIL [{lab}]: took {os.environ['ELAPSED']}s; fixed-timeout waiting is back"
print(f"PASS [{lab}]: completed in {os.environ['ELAPSED']}s with no fixed-timeout stalls")
EOF
  L=$(cat "$T"/OneIdentity/*/azure_*.log)
  grep -q 'S3cr3t' <<<"$L" && { echo "FAIL [$3]: password in log"; exit 1; }
  grep -q '123456\|000001' <<<"$L" && { echo "FAIL [$3]: OTP code in log"; exit 1; }
  echo "PASS [$3]: neither password nor OTP code in log"
}

run_case ""                  false "code page is the default"
run_case "?push=1"           true  "push approval is the default"
# The reported symptom: the Sign in button swallows the synthetic click, so the
# form sits there looking like it is waiting for the user to press Enter.
run_case "?stubborn=1"       false "sign-in button ignores the click"
run_case "?stubborn=1&push=1" true "button ignores the click, push default"

# A rejected password also leaves the field on screen. The Enter fallback must
# not fire there, or every failed sign-in costs two attempts and walks the
# account towards a lockout.
echo "--- rejected password must not be resubmitted"
rm -f "$T/state.json"; curl -s "http://127.0.0.1:$PORT/reset" >/dev/null
NOW=$(date +%s)
printf '{"username":"alice@contoso.onmicrosoft.com","password":"S3cr3t!pa ss","asset":"127.0.0.1:%s","Target.TotpCodes":[{"Code":"000001","UnixTime":%s,"Period":30}],"cli_args":"-url http://127.0.0.1:%s/?badpass=1 -no-wait -step-timeout 25s"}' \
  $PORT $((NOW-2)) $PORT | DISPLAY="$TEST_DISPLAY" APPDATA="$T" "$T/azure" >"$T/bad.txt" 2>&1 || true
python3 - "$T/state.json" <<'EOF'
import json,sys
s=json.load(open(sys.argv[1]))
assert s["submits"]==1, f"FAIL: rejected password submitted {s['submits']} times, expected 1"
print("PASS: rejected password submitted exactly once (no lockout risk)")
EOF
[ -n "${XV:-}" ] && kill $XV 2>/dev/null || true
echo "ALL PASS"
