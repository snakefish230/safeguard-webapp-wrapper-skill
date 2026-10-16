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
mock Entra whose code page deliberately carries the link and rejects the first code.

SPP setup: asset NetworkAddress = tenant domain (e.g. `contoso.onmicrosoft.com`); account name = UPN
prefix or full UPN. Push / number-matching MFA cannot be injected; the user approves on their phone.
Note: Microsoft is rolling out a redesigned sign-in UX; re-probe with
`go run ./tools/probe -url https://portal.azure.com -step 'type:input[name=loginfmt]:<real-upn>' -step 'click:#idSIButton9'`
(using a real UPN shows the password page; never type a real password). Pass
`-chrome <path>` when Chrome for Testing isn't at the default container path.
