# Azure portal (Entra ID, cloud-only user)

Wrapper: `wrapper/sites/azure`. Start URL `https://portal.azure.com` → redirects to login.microsoftonline.com.

| Page | Selector | Verified |
|---|---|---|
| Username | `input[name=loginfmt]` (`#i0116`), Next `#idSIButton9` | 2026-10-16 live probe |
| Account picker (some tenants) | `#aadTile` (optional) | upstream; absent on safeguardlabs tenant 2026-10-16 |
| Password | `input[name=passwd]` (`#i0118`), Sign in `#idSIButton9` | 2026-10-16 live probe + wrapper smoke test |
| TOTP | `#idTxtBx_SAOTCC_OTC` / `input[name=otc]`, `#idSubmit_SAOTCC_Continue` | upstream sample |
| Stay signed in? | No `#idBtn_Back`, Yes `#idSIButton9` | upstream sample |

**`#idBtn_Back` is ambiguous.** It is the *Back* button on the password page and *No* on the
"Stay signed in?" page (confirmed by live probe 2026-10-16). Clicking it optionally straight
after the password submit can race the page transition and navigate backwards, so the wrapper
runs `core.WaitGone(passSel, 20s)` first: the password field must disappear before any
following optional click. On a wrong password the field stays, the wait burns its full timeout
and no false "Stay signed in?" click can corrupt a good session.

SPP setup: asset NetworkAddress = tenant domain (e.g. `contoso.onmicrosoft.com`); account name = UPN
prefix or full UPN. Push / number-matching MFA cannot be injected; the user approves on their phone.
Note: Microsoft is rolling out a redesigned sign-in UX; re-probe with
`go run ./tools/probe -url https://portal.azure.com -step 'type:input[name=loginfmt]:<real-upn>' -step 'click:#idSIButton9'`
(using a real UPN shows the password page; never type a real password). Pass
`-chrome <path>` when Chrome for Testing isn't at the default container path.
