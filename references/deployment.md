# Deploying a wrapper

## Automated (remote)
From a Windows admin workstation with WinRM access to the jump host:
```powershell
cd scripts
./publish-remoteapp.ps1 -ComputerName jumprdp.corp.local -Wrapper ..\dist\aws.exe `
  -Alias AWSConsole -DisplayName "AWS Console" -RemoteDir C:\OI_Custom `
  -WrapperArgs '-set account=123456789012' `
  -Credential (Get-Credential) -WhatIf      # drop -WhatIf to apply
./publish-remoteapp.ps1 -ComputerName jumprdp -Alias AWSConsole -Remove
```

### Which store gets written (`-Mode`)
`-Mode Auto` (default) detects this and prints what it chose. Don't override it without reason:
publishing to the wrong store looks like it succeeded but the app never appears.

| Host | Mode | Store |
|---|---|---|
| Standalone jump host, no RDS deployment | `Registry` | `TSAppAllowList\Applications\<Alias>` |
| Full RDS deployment (a collection exists) | `Collection` | broker → `CentralPublishedResources\PublishedFarms\<farm>\Applications\<Alias>` |

On a deployment host the broker owns `TSAppAllowList` and rewrites it from the collection, so a
hand-written entry there publishes nothing. `-Mode Registry` refuses on such a host unless `-Force`.

Detection reads the registry and `root\cimv2\rdms` directly, **not** the RDMS cmdlets: over WinRM
those need a second hop to the broker and fail misleadingly with "A Remote Desktop Services
deployment does not exist" / "RD Connection Broker server is not available" on a host that plainly
has one. For the same reason Collection mode runs `New-/Set-RDRemoteApp` from a short-lived SYSTEM
scheduled task on the target (SYSTEM authenticates as the computer account) — no CredSSP, no
password on disk.

Two Collection-mode quirks the script handles:
- `New-RDRemoteApp` aborts with "Could not find the specified icon" when the launcher exe has no
  icon resource; it retries with `-FallbackIconPath` (default `imageres.dll`).
- It always passes `-Alias`. Letting RDS generate one produces e.g. `OISGRemoteAppLauncher(1)`,
  which differs from an existing `OISGRemoteAppLauncher (1)` only by a space. After publishing it
  re-reads the app and fails if the stored alias or command line isn't what was asked for.

The alias must match the SPP policy's **Application Alias** exactly, as `||<Alias>`. Hosts
published with the Remote Application Publisher name every app after the launcher exe, giving
`OISGRemoteAppLauncher`, `OISGRemoteAppLauncher (1)`, `(2)`, ... — match the host's convention.

Credentials from Safeguard instead of `-Credential` (needs `Install-Module safeguard-ps`):
```powershell
./publish-remoteapp.ps1 -ComputerName 10.0.7.103 -Wrapper ..\dist\azure.exe -Alias AzurePortal `
  -DisplayName "Azure Portal" -SafeguardAppliance spp.safeguardlab.net `
  -SafeguardAsset 10.0.7.103 -SafeguardAccount Administrator -CreateAccessRequest
```
Reuses an open Password request for that account or creates one. It checks the password out
straight into a PSCredential (never displayed) and checks it in at exit (`-SafeguardKeepRequest`
to keep it open). Use `-SafeguardAuth Certificate -SafeguardCertThumbprint <tp>` for
non-interactive runs.

Preflight checks: the Launcher exists (`-LauncherPath` if it's installed somewhere else),
Chrome or Edge is present, and the alias isn't already taken (`-Force`). The folder gets a
locked-down ACL (Admins/SYSTEM full, Users read+execute) so a user can't swap the exe.

## Manual: jump host (RDS)
1. Install Chrome (or use Edge), the OI-SG-RemoteApp-Launcher, and the
   [Remote Application Publisher](https://github.com/OneIdentity/RemoteApplicationPublisher).
2. Copy `<site>.exe` to the host's wrapper directory — ask the user; the script default is
   `C:\SafeguardWrappers`, but many hosts use something else (e.g. `C:\OI_Custom`). Verify it
   against `<site>.exe.sha256` (`Get-FileHash`), and restrict write access (Administrators only).
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
