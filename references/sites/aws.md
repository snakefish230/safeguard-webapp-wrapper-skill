# AWS Management Console

Wrapper: `wrapper/sites/aws` (originally from upstream `aws.go`; ported to core + stdin).

| Flow | URL | Selectors |
|---|---|---|
| IAM user | `https://<account>.signin.aws.amazon.com/console` | `#username`, `#password`, `#signin_button`; MFA `#mfacode` → `#submitMfa_button` |
| Root | `https://signin.aws.amazon.com/console` | `#root_user_radio_button` (optional), `#resolving_input`, `#next_button`, then `#ap_password`+`#signInSubmit-input` **or** newer `#password`+`#signin_button`; MFA `#ap_tokenCode` or `#mfacode` |

Status: selectors come from One Identity's upstream sample and haven't been re-verified live
against current AWS sign-in pages. Re-verify with browser_exec before the first deployment and
update this file with the date. AWS also shows a CAPTCHA after repeated failures; automation
can't solve it.
