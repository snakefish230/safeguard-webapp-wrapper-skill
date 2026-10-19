# Testing a wrapper

There is one failure mode that matters here, and the project hit it hard: **a
test suite that passes while production is broken.** Everything below exists to
prevent that.

## The rule: a mock must never be kinder than the real site

The Entra mock used to hide each view as it moved to the next, so the field the
wrapper had just filled genuinely disappeared. Real Entra does not do that — it
*parks* the previous view, leaving the username box in the DOM and still
reporting a visible box on the password page.

Because the mock was gentler than reality, a success check of "wait for the
field to disappear" passed every time in tests and failed every time in
production. The suite was green for days while sign-in was unusable.

The mock now leaves the parked view **fully visible and interactive** — the
worst case, and the one that was actually measured. When you model a site,
model the inconvenient behaviour:

- Does the site remove the old view, or park it? Park it in the mock.
- Does a button respond to a synthetic click? `?stubborn=1` models one that
  does not.
- Does a wrong password clear the field, or leave it? Leave it (`?badpass=1`).

If you are unsure which way the real site behaves, model the version that is
harder to pass. A wrapper that survives the pessimistic mock survives both.

## The rule: confirm the test fails without the fix

A test written after a fix usually passes for the wrong reason. Before
believing it, break the fix and watch the test fail:

```bash
# revert just the fix, keep the test
sed -i 's/core.Submit(passSel, nextBtn, passNext, passErr/core.Submit(passSel, nextBtn, "", passErr/' \
  wrapper/sites/azure/main.go
tests/e2e/run-azure-otp.sh     # must fail, and ideally with the reported symptom
git checkout wrapper/sites/azure/main.go
```

The `Submit` fix was verified this way twice. The first attempt passed the
suite while still being wrong, and only the deliberate-failure check exposed it.
When the suite failed it took 54s and reported the user's exact symptom, which
is the strongest signal you can get that a regression test is real.

Assert on **elapsed time**, not just the outcome. Both of the serious bugs here
showed up as "it works, slowly" long before they showed up as "it fails", and a
mock renders instantly so any multi-second step is a stall:

```python
assert int(os.environ["ELAPSED"]) < 20, f"took {os.environ['ELAPSED']}s; fixed-timeout waiting is back"
```

## The rule: assert the site and the wrapper agree

The mock recording a successful sign-in and the wrapper *believing* it succeeded
are separate facts. Assert both, or a wrapper that misreads success looks like
a pass:

```bash
if grep -q "Automatic sign-in failed" "$T/out.txt"; then
  echo "FAIL: site accepted the code but the wrapper reported failure"; exit 1; fi
```

This caught a real bug: after the OTP was accepted, the parked OTP view kept the
code box on screen, so the wrapper read its own success as a rejection.

## What ships

| File | Covers |
|---|---|
| `tests/e2e/run.sh` | The generic wrapper against a plain 3-step mock. Run after **any** change to `core/`. |
| `tests/e2e/run-azure-otp.sh` | The Entra flow: code page vs push default, swallowed clicks, a rejected password, and TOTP retry. |
| `tests/e2e/mocksite.py` | The plain login mock. |
| `tests/e2e/entra_otp.py` | The Entra mock, including the parked-view behaviour. |
| `wrapper/core/core_test.go` | Unit tests for input parsing and TOTP selection. |

Both e2e scripts need a real browser and an X display. They use `$DISPLAY` if
Xvfb is unavailable, which flashes a browser window on your desktop — expected,
not a bug. Set `CHROME=<path>` if Chrome is not on `PATH`.

## Adding a case

`run-azure-otp.sh` takes `run_case <query> <expect_detour> <label>`; the query
selects a mock scenario. A case that cannot satisfy the standard assertions —
`?badpass=1` never reaches the OTP step — needs its own assertion block rather
than a loosened shared one. Keep the strict assertions strict.

Secrets: every case asserts that neither the password nor an OTP code appears in
the wrapper's log. Keep that assertion in any new case; it is the only automated
check that `-debug` stays safe to enable in production.
