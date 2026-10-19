# Azure portal (Entra ID, cloud-only user)

Wrapper: `wrapper/sites/azure`. Start URL `https://portal.azure.com` → redirects to login.microsoftonline.com.

| Page | Selector | Verified |
|---|---|---|
| Username | `input[name=loginfmt]` (`#i0116`), Next `#idSIButton9` | 2026-10-16 live probe |
| Account picker (some tenants) | `#aadTile` (optional) | upstream; absent on safeguardlabs tenant 2026-10-16 |
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
