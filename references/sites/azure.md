# Azure portal (Entra ID, cloud-only user)

Wrapper: `wrapper/sites/azure`. Start URL `https://portal.azure.com` → redirects to login.microsoftonline.com.

| Page | Selector | Verified |
|---|---|---|
| Username | `input[name=loginfmt]` (`#i0116`), Next `#idSIButton9` | 2026-10-16 live probe |
| Account picker (some tenants) | `#aadTile` (optional) | upstream; absent on the tenant tested 2026-10-16 |
| Password | `input[name=passwd]` (`#i0118`), Sign in `#idSIButton9` | 2026-10-16 live probe + wrapper smoke test |
| TOTP | `#idTxtBx_SAOTCC_OTC` / `input[name=otc]`, `#idSubmit_SAOTCC_Continue` | 2026-10-16, see below |
| Stay signed in? | No `#idBtn_Back`, Yes `#idSIButton9` | upstream sample |

**`#idBtn_Back` is ambiguous.** It is the *Back* button on the password page and *No* on the
"Stay signed in?" page (confirmed by live probe 2026-10-16). Clicking it optionally straight
after the password submit can race the page transition and navigate backwards, so the wrapper
runs `core.WaitGone(passSel, 20s)` first: the password field must disappear before any
following optional click. On a wrong password the field stays, the wait burns its full timeout
and no false "Stay signed in?" click can corrupt a good session.

## Two OTP faults, found 2026-10-16

**`#signInAnotherWay` is on the verification-code page too.** It is the "I can't use my
Microsoft Authenticator app right now" link, so it exists both on the push-approval page
*and* on the page the wrapper is trying to reach. Clicking it unconditionally made a tenant
whose default method is already a verification code detour through the method picker and come
back — visible to the user as the sign-in "jumping to another method then returning". The
wrapper now uses `core.ClickIfPresentUnless(sel, otpSel, ...)`: the detour is taken only when
the OTP box is *not* already on screen.

**A TOTP code must be picked after the field appears, not before.** `core.TOTP` previously
chose the code and then waited for the field, so every second of page transition came out of
the code's lifetime; the detour above added ~8s to a code picked with only 5s of headroom, and
a perfectly correct code arrived expired. `core.TOTPSubmit` waits for the field first, then
picks, types and submits, then confirms the field went away. If it is still there the code was
rejected, and a *different* code from the `Target.TotpCodes` snapshot is tried (default 2
attempts, `-totp-attempts`). `-totp-min-valid` now defaults to 10s.

Both faults are pinned by `tests/e2e/run-azure-otp.sh`, which drives the real wrapper against a
mock Entra whose code page deliberately carries the link and rejects the first code. It runs
the scenario twice: once with the code page as the default and once with push approval as the
default, so the detour is proven both to be skipped and to still work when it is needed.

## Removing the dead waiting, 2026-10-16

The fixes above were correct but slow, because each optional step waited out a fixed timeout
before testing the alternative. A debug run showed 26s on a path that needed 3s:

- **20s** was spent after a rejected code, waiting for the OTP field to vanish. It never would.
  `TOTPSubmit` now races the field disappearing (accepted) against `#idSpan_SAOTCC_Error_OTC`
  appearing (refused), so the retry starts at once. The error must be probed *before* the
  field: a refused code leaves the field on screen, so checking the field first matches every
  time and hides the error. An error left over from the previous attempt is ignored — typing
  normally clears it, and if it is still up the wrapper falls back to detecting success only.
- **3s** was a fixed `#aadTile` probe on tenants with no account picker. It is now raced
  against the password field, just as the "another way" link is raced against the OTP box
  (`core.WaitAny`). No branch pays a timeout it does not need.

The mock also caught the wrapper reporting failure on a code the site had accepted, so the
test now asserts both facts: the site accepted the code *and* the wrapper agreed.

## Keystrokes can be dropped

A mock run produced username `lice@contoso...` — the leading `a` was lost, because the first
raw keystroke goes nowhere if the browser has not finished focusing the field. Silently
truncating a username or password surfaces much later as a failed sign-in, so `Type`/`Secret`
now read the field back and, on mismatch, assign through the `value` prototype setter and
dispatch `input`/`change` (Entra's sign-in is React, so a plain `e.value =` is ignored).
Values are still never logged — only lengths, and only on mismatch.

## The step that never looked finished

Reported as: the password is typed, then nothing happens until the user presses Enter.

My first explanation was that `chromedp.Click` dispatches at the element's coordinates and so
gets lost when Entra re-renders. That was a guess, and it was wrong. A `-debug` log from a
real run settled it in four lines:

```
15:42:40 "form did not advance on click, pressing Enter"  field=loginfmt
15:43:00 "form still showing after Enter"                 field=loginfmt
15:43:00 "already on target page, skipping optional click" skip=passwd
15:43:00 secret input[name="passwd"] -> ok in 73ms
```

The page **had** advanced — the password field was right there. **Entra parks the view you
came from instead of removing it, so `input[name="loginfmt"]` is still in the DOM and still
reports a visible box while the password page is up.** Every check of the form "has the field
gone" therefore answered *no*, on every step, forever.

The cost was not just a spurious Enter. Each step burned its full fallback budget, roughly 23s
twice over, and SPP mints the TOTP codes at session start with 30s lives. By the time the
wrapper reached the code box every code was dead:

```
15:33:01 step failed step=8 err="no unused TOTP code valid for >= 10s"   (after 2m35s)
```

So "the OTP is wrong" and "it waits for Enter" were the same fault.

### What replaced it

Absence is not a usable success signal — ask the positive question instead. `core.Submit` takes
a `next` selector naming what we expect on the other side, and `TOTPSubmit`/`otpOutcome` take
`okSel`. Each lists every page that step can legitimately land on, so no branch pays a timeout:

| step | positive signal |
|---|---|
| username | password field, or the account-type tile |
| password | OTP box, "sign in another way", the method picker, or "Stay signed in?" |
| OTP | "Stay signed in?" (`#KmsiCheckboxField`) |

`errSel` still suppresses the Enter fallback, so a rejected credential is never resubmitted
into a lockout.

Two supporting fixes in `probeVisible`, both found by this bug:

- **Visibility now means reachable**, not merely "has a box": inside the viewport, not under
  `aria-hidden`, and the element `elementFromPoint` actually returns at its own centre. A
  parked view fails all three.
- **`querySelectorAll`, not `querySelector`.** These are comma-joined groups, and the first
  element in document order is routinely a hidden copy on a view not yet reached — the hidden
  `#signInAnotherWay` in the push view masked the visible OTP box. Testing only the first match
  reported the whole group absent. This one cost a wrong fix before it was spotted.

### Testing it

`tests/e2e/entra_otp.py` used to hide the previous view, which made the mock kinder than the
real site and let the broken "wait for it to disappear" logic pass. It now leaves the parked
view **fully visible and interactive** — the worst case, and the one actually measured. Drop
the `next` selectors and the suite fails in 54s with the real symptom; with them it passes in
4s.

## Launching it

A correct wrapper and policy can still fail to start from the SPP GUI. The Azure asset is
deliberately named `Azure`, not `Azure portal`: the Entra account is a long UPN
(`user@tenant.onmicrosoft.com`), and the two together pushed SPP's RDP `username` field to
257 characters, past the protocol's 255-character limit. `mstsc` then refused with `0x904`
"can't connect" — which reads as a network fault but is not. Keep this asset's name short.
See `references/scalus-launch.md`.

SPP setup: asset NetworkAddress = tenant domain (e.g. `contoso.onmicrosoft.com`); account name = UPN
prefix or full UPN. Push / number-matching MFA cannot be injected; the user approves on their phone.
Note: Microsoft is rolling out a redesigned sign-in UX; re-probe with
`go run ./tools/probe -url https://portal.azure.com -step 'type:input[name=loginfmt]:<real-upn>' -step 'click:#idSIButton9'`
(using a real UPN shows the password page; never type a real password). Pass
`-chrome <path>` when Chrome for Testing isn't at the default container path.
