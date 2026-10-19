---
name: safeguard-webapp-wrapper
description: "Use when building a Safeguard RDP-app web wrapper (Go/chromedp .exe) for a site. Generates, tests, builds and documents a Windows exe that the OI-SG-RemoteApp-Launcher runs on the RDS jump host to open a browser and inject Safeguard credentials."
version: 1.3.0
metadata:
  hermes:
    tags: [safeguard, one-identity, pam, rdp-applications, chromedp, go]
---

# Safeguard Web App Wrapper Builder

Produces `dist/<site>.exe`: a Windows wrapper that the **One Identity Safeguard
RemoteApp Launcher** (`OI-SG-RemoteApp-Launcher.exe`) starts on the RDS jump host.
It launches Chrome/Edge, opens the login URL, injects the credential released by
Safeguard (username/password/TOTP), and stays alive until the browser is closed,
so the SPS recording covers the whole session.

Project (git-tracked): `/opt/data/sync/projects/safeguard-webapp-wrapper-skill/`
Upstream reference: https://github.com/OneIdentity/SafeguardAutomation (`RDP Applications/Go chromedp`).

## Layout
```
wrapper/core/        shared runtime: stdin/flag input, browser launch, step helpers, TOTP
wrapper/sites/<name>/main.go   one wrapper per site (aws, generic, _template)
scripts/publish-remoteapp.ps1 copy+verify+register RemoteApp on jump host via WinRM
scripts/build.sh <site>|all    vet + unit tests + GOOS=windows build -> dist/<site>.exe(+.sha256)
tests/e2e/run.sh     real Chrome + Xvfb against a mock 3-step login (proves the core works)
references/          launcher contract, deployment, scalus-launch, safeguard-config (MCP/API), per-site selectors
```

## Workflow (for "build a wrapper for <site>")

1. **Gather facts** (ask only if not inferable): login URL (fixed SaaS URL vs. per-asset
   `https://{asset}/...`), tenant/account identifier and where it comes from (asset address
   vs. `-set key=value` in `--args`), username format (`{username}`, UPN
   `{username}@{Target.AccountDomainName}`), MFA (TOTP via `Target.TotpCodes`? push/FIDO can't
   be automated — user completes it), Chrome or Edge, self-signed cert (`-insecure`).
2. **Discover selectors live** with `browser_exec`: open the login URL, walk each page
   (username → Next → password → MFA), and for each input/button record a stable CSS selector
   (prefer `#id`, `[name=]`, `[data-testid=]`, `[autocomplete=username]`; avoid generated
   classes). Note page transitions and optional interstitials (cookie banner, account picker,
   "Stay signed in?"). Never submit real credentials during discovery. Record findings in
   `references/sites/<site>.md` with the date.
3. **Generate the site**: copy `wrapper/sites/_template/` → `wrapper/sites/<site>/`, remove the
   `//go:build ignore` line, fill in URL + steps using core helpers:
   `Type`, `Secret` (not logged), `Click`, `Enter`,
   `Submit(field, btn, errSel, d)` to advance a login form (prefer it over bare `Click` —
   see the third MFA rule below), `ClickIfPresent(sel, d)` for optional
   prompts, `ClickIfPresentUnless(sel, skip, skipWait, d)` when that optional control also
   exists on the page you are heading for, `FirstVisible(map[sel]action)` for branching forms,
   `TOTPSubmit(sel, btn, json, minValid, attempts)`,
   `WaitVisible`, `WaitURLContains`. Site-specific non-secret options become flags
   (`fs.String/Bool`), read after `core.Load`. If the site fits a plain form, just emit a
   `generic.exe` command line instead of new code.

   Two MFA rules, both learned the hard way on Entra (see `references/sites/azure.md`):
   - **Never pick a TOTP code before its field is visible.** The code has ~30s of life and any
     page transition you wait through is spent out of it, so a correct code arrives expired.
     `TOTPSubmit` waits, then picks. Prefer it over bare `TOTP`.
   - **A "use another method" link is often on the code page as well.** Clicking it
     unconditionally detours through the method picker and back. Gate it on the OTP field not
     already being present.
   - **A click on a sign-in button is not a reliable submit.** `chromedp.Click` dispatches at
     the element's coordinates, so it is lost whenever the page re-renders or animates between
     hit-test and dispatch. The click reports success and the form just sits there, looking to
     the user exactly like it is waiting for them to press Enter. `Submit` clicks, waits a
     short grace for the field to go, then sends Enter into the field itself. Pass `errSel`:
     a *rejected* credential also leaves the field on screen, and resubmitting there spends a
     second attempt and walks the account towards a lockout, so `Submit` stays its hand when
     that error is visible.
4. **Build & test**: `scripts/build.sh <site>`; run `tests/e2e/run.sh` after any change to
   `core/`. Optionally smoke-test the site wrapper on Linux against the real login page with
   dummy creds (build without GOOS, pipe stdin JSON, `DISPLAY` via Xvfb, Chrome at
   `/opt/data/tools/chrome-linux64/chrome` wrapped with `--no-sandbox`) and confirm it reaches
   the "invalid password" state, which proves every selector before the final submit.
5. **Deliver**: `dist/<site>.exe` + `.sha256`, the Launcher publishing line, and the SPP
   policy notes from `references/deployment.md`. Commit with git (`git add -A && git commit`).
6. **Publish (optional, remote)**: `scripts/publish-remoteapp.ps1` copies the exe to the jump
   host over WinRM, verifies its SHA256, locks down the folder ACL, and registers the Launcher
   as a RemoteApp (Registry mode = standalone host, Collection mode = RDS Connection Broker).
7. **Check how the user will actually launch it**: a correct policy still fails if the RDP
   `username` SPP builds exceeds 255 characters — `mstsc` then reports `0x904`
   "can't connect", which looks like a network fault and is not. The `rdp://` URL used by
   "Start RDP Session" is ~29 chars longer than the downloaded `.rdp`, so the download can
   work while the GUI button fails. Keep asset names short for cloud targets with long UPNs.
   See `references/scalus-launch.md`.
   It prints the SPP Application Alias (`||<Alias>`) and Display Name. Run it from a Windows
   admin box with WinRM to the host. Supports `-WhatIf`, `-Force` (overwrite), and `-Remove`.

   **Ask where the wrapper should live on the jump host** (`-RemoteDir`). The script's default
   is `C:\SafeguardWrappers`, but sites commonly keep wrappers somewhere else (e.g.
   `C:\OI_Custom`) and all wrappers on a host should sit together. Confirm the directory with
   the user before publishing, and use the same path in the Launcher `--cmd` line.

   **Credential for the jump host: use the Safeguard checkout, never chat.** Ask the user:
   *"Which SPP account should be used to administer the RDS host (name, and the asset it's on)?"*
   Then use the Safeguard MCP to check requestability. These are read-only calls and return
   metadata only:
   - `GET /v4/Me/AccessRequestAssets?filter=Name eq '<asset>' or NetworkAddress eq '<asset>'` → assetId
   - `GET /v4/Me/RequestEntitlements?assetIds=<assetId>&accessRequestType=Password` → is the
     account listed (Account.Name)? Which policy?
   - `GET /v4/Me/ActionableRequests`, or `GET /v4/AccessRequests?filter=AccountName eq '<acct>' and AccessRequestType eq 'Password'`
     and look for State RequestAvailable/PasswordCheckedOut/PendingApproval/Approved → is there
     already an open request?

   Then branch:
   - **Open request exists** → tell the user its ID/state; the script will reuse it.
   - **Requestable, no open request** → ask *"Create a Password access request for <acct>?"*
     If yes, pass `-CreateAccessRequest` to the script, which creates it under the user's own
     identity so the audit trail is right. If the policy needs approval, say so; the script exits
     12 and can be re-run after approval.
   - **Not requestable** (no policy grants the user Password access) → ask *"There's no access
     policy that lets you check out <acct>. Create one?"* If yes, follow
     `references/safeguard-config.md` → "Jump host credential policy", confirming each POST.
     Then continue as above.

   Then give the user the command to run. The checkout happens inside the script with
   `safeguard-ps` (password → SecureString → PSCredential). It's never printed and is checked
   in at exit:
   ```powershell
   ./publish-remoteapp.ps1 -ComputerName 10.0.7.103 -Wrapper ..\dist\azure.exe -Alias AzurePortal `
     -DisplayName "Azure Portal" -SafeguardAppliance spp.example.com -SafeguardAsset 10.0.7.103 `
     -SafeguardAccount Administrator [-CreateAccessRequest] [-SafeguardAuth Browser|DeviceCode|Certificate]
   ```
   `-SafeguardStatusOnly` prints requestability and the open request as JSON, a quick pre-check
   without the MCP. Exit codes: 10 not requestable, 11 no request and no
   `-CreateAccessRequest`, 12 pending approval.
   **Never** use `Safeguard_RetrieveCredential` or `POST .../CheckOutPassword` through the MCP
   for this. Hermes doesn't honour MCP `audience` annotations, so the password would land in
   the model context and transcript.

7. **Configure Safeguard (optional, needs the Safeguard MCP)**. Follow
   `references/safeguard-config.md`. Check that a `Safeguard_*` MCP tool exists first; if it
   doesn't, explain how to install https://github.com/OneIdentity/safeguard-mcp and stop this
   step. Ask the user which appliance (MCP server) to use, then `Safeguard_Connect`. Make the
   decisions below in **one** clarify call where possible:
   - Target asset/account for the web app: existing (look it up and confirm) or **create**?
   - Jump host asset/account: existing or **create**?
   - Entitlement: **new** (name + members) or **add to existing** (pick from list)?
   - Approval required? Release duration? Reason comment required?
   Then: Step 0 lookups → create any missing assets/accounts → role → access policy (type
   `RemoteDesktopApplication`, ApplicationDisplayName/Alias from step 6) → scope item for the
   target account → verify `InvalidConnectionPolicy=false`. Show each POST body to the user and
   get confirmation before running any write. Record the IDs you create so a rollback never
   touches existing objects.

## Launcher contract (summary — full details in references/launcher-contract.md)
Publish the **Launcher** as the RemoteApp, wrapper as `--cmd`:
```
--use-stdin --cmd "C:\SafeguardWrappers\aws.exe" --args "-set account=123456789012 -incognito"
```
With `--use-stdin` (Launcher ≥ 3.0.1) the Launcher writes one JSON line to stdin containing
`username`, `password`, `asset`, `Target.*` (e.g. `Target.AssetNetworkAddress`,
`Target.AccountDomainName`, `Target.TotpCodes`) and `cli_args` (= `--args`). core parses
`cli_args` as the wrapper's flags. Legacy mode (`--args "-username {username} -password
{password} ..."`) still works but exposes the password in the process list. Don't use it.

## Rules / pitfalls
- **Pin chromedp v0.14.2.** v0.21+ rewrote `Action` as a generic type, so the classic API and
  the upstream One Identity samples no longer compile. Building from a stale go.sum mixes
  versions; if that happens, reset go.mod/go.sum and run `go get chromedp@v0.14.2`.
- `chromedp.SetValue(sel,"")` failed on real Chrome ("could not set value on node"); use
  `chromedp.Clear`.
- With `-no-wait` the process exits (killing the browser) right after the last step. Add a
  `WaitVisible`/`WaitURLContains` success step so the final submit actually lands. In
  production, leave the default wait-for-browser-close.
- Never log values; `core` logs only key names. Never set `dumpStdinToLog`-style options.
- Default to incognito/InPrivate so no session or cookie carries over between users on the
  shared jump host; the browser is also told not to offer to save passwords.
- A failed step leaves the browser open for manual completion instead of exiting, so the
  user isn't dropped from the session.
- Push/FIDO/number-matching MFA can't be injected. Stop after password and let the user approve.
- Windows Defender may flag freshly built Go exes (seen upstream); sign them or add an exclusion.
- WinRM to an **IP address** uses NTLM. The admin box needs `Set-Item WSMan:\localhost\Client\TrustedHosts <ip>`
  (or HTTPS listener + `-UseSSL`); Kerberos/keytab doesn't work against a bare IP. The script
  logs in as `<ComputerName>\<account>` for local accounts (override with `-SafeguardLoginName`).
- publish-remoteapp.ps1 refuses `-password`/`{password}` and double quotes in -WrapperArgs.
  Credentials always go via `--use-stdin`. Legacy `--args "{username} {password}"` substitution
  puts the real password on a command line, which is exposed to any process listing
  (`Win32_Process`, Task Manager) and captured by 4688-with-command-line, Sysmon and most EDR;
  `--use-stdin` keeps it on an in-process pipe. Existing apps on a host may still use the legacy
  form — check before assuming the host is clean. It sets `CommandLineSetting=2`, so only the exact
  registered command line can run. Registry value names follow standard Windows RemoteApp
  behaviour; diff them against one app made by the Publisher the first time it's used.
- pwsh for syntax checks: `/opt/data/tools/bin/pwsh` with `DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1`
  (no libicu). Parser-only; remoting can't run from the container.
- Safeguard config: **never** set passwords or TOTP seeds through the MCP or chat. The user
  enters them in the SPP UI; verify with `HasPassword` / `HasTotpAuthenticator`. Call
  `Safeguard_Schema` before the first POST on each appliance, because fields vary by SPP
  version. Entitlement = `/v4/Roles`. RDP-app settings live in
  `SessionProperties.RemoteDesktopApplicationProperties`, and the alias has a `||` prefix.
- Toolchain on this host: Go at `/opt/data/tools/bin/go` (GOPATH/GOCACHE under
  `/opt/data/tools`), Chrome for Testing at `/opt/data/tools/chrome-linux64/`. Use `Xvfb :97`
  directly because `xvfb-run` fails (no `xauth`).
