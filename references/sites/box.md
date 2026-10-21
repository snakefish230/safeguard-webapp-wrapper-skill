# Box (account.box.com) — verified 2026-10-21

Two-step native login at `https://account.box.com/login`:

| Step | Selector |
|---|---|
| Email | `#login-email` (name=login, autocomplete=username) |
| Next | `#login-submit` |
| Password | `#password-login` (name=password) |
| Log In | `#login-submit-password` |

Notes: MFA not automated (push/SMS/TOTP prompts are completed by the user). SSO-enabled
Box enterprises redirect to the IdP and need a different wrapper. Username format
`{username}@{Target.AccountDomainName}` is resolved by SPP or by the wrapper's `-domain`.
