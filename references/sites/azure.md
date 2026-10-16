# Azure portal (Entra ID, cloud-only user)

Wrapper: `wrapper/sites/azure`. Start URL `https://portal.azure.com` → redirects to login.microsoftonline.com.

| Page | Selector | Verified |
|---|---|---|
| Username | `input[name=loginfmt]` (`#i0116`), Next `#idSIButton9` | 2026-10-14 live probe |
| Account picker (some tenants) | `#aadTile` (optional) | upstream |
| Password | `input[name=passwd]` (`#i0118`), Sign in `#idSIButton9` | upstream sample, not yet live |
| TOTP | `#idTxtBx_SAOTCC_OTC` / `input[name=otc]`, `#idSubmit_SAOTCC_Continue` | upstream sample |
| Stay signed in? | No `#idBtn_Back`, Yes `#idSIButton9` | upstream sample |

SPP setup: asset NetworkAddress = tenant domain (e.g. `contoso.onmicrosoft.com`); account name = UPN
prefix or full UPN. Push / number-matching MFA cannot be injected; the user approves on their phone.
Note: Microsoft is rolling out a redesigned sign-in UX; re-probe with
`go run ./tools/probe -url https://portal.azure.com -step 'type:input[name=loginfmt]:<real-upn>' -step 'click:#idSIButton9'`
(using a real UPN shows the password page; never type a real password).
