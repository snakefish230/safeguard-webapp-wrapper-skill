# Deploying a wrapper

## Jump host (RDS)
1. Install Chrome (or use Edge), the OI-SG-RemoteApp-Launcher, and the
   [Remote Application Publisher](https://github.com/OneIdentity/RemoteApplicationPublisher).
2. Copy `<site>.exe` to e.g. `C:\SafeguardWrappers\`, verify it against `<site>.exe.sha256`
   (`Get-FileHash`), and restrict write access (Administrators only).
3. Publisher → New app: Name `AWSConsole`, Full Name `AWS Console`, enable **Use One Identity
   Launcher**. Command line parameters:
   `--use-stdin --cmd "C:\SafeguardWrappers\aws.exe" --args "-set account=123456789012"`
   (Add `--enable-debug` and `-debug` while troubleshooting.)
4. If Defender quarantines the exe, sign it or add a path exclusion.

## SPP
1. Add the jump host as an asset and its RDP account.
2. Add the target as an asset and account (for SaaS, e.g. an "Other Managed" asset whose
   network address is the AWS account id/alias; use `-account-from-asset`). Enable a TOTP
   authenticator on the account if MFA is required.
3. Entitlement → Access Request Policy: Policy type **Session**, Session type **RDP Application**.
   Security tab: SPS connection policy, RDP Host Asset/Account = jump host, Application Display
   Name = Publisher *Full Name*, Application Alias = Publisher *Program Path* (`||AWSConsole`).
   Scope: the target account.
4. Request → Launch (SCALUS) or Download RDP. Only the browser appears, already signed in.
