---
name: safeguard-webapp-wrapper
description: "Use when building a Safeguard RDP-app web wrapper (Go/chromedp .exe) for a site. Generates, tests, builds and documents a Windows exe that the OI-SG-RemoteApp-Launcher runs on the RDS jump host to open a browser and inject Safeguard credentials."
version: 1.0.0
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
references/          launcher contract, deployment, per-site selector notes
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
   `Type`, `Secret` (not logged), `Click`, `Enter`, `ClickIfPresent(sel, d)` for optional
   prompts, `FirstVisible(map[sel]action)` for branching forms, `TOTP(sel, json, minValid)`,
   `WaitVisible`, `WaitURLContains`. Site-specific non-secret options become flags
   (`fs.String/Bool`), read after `core.Load`. If the site fits a plain form, just emit a
   `generic.exe` command line instead of new code.
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
   It prints the SPP Application Alias (`||<Alias>`) and Display Name. Run it from a Windows
   admin box with WinRM to the host; don't try to run it from this container. Example:
   `./publish-remoteapp.ps1 -ComputerName jumprdp -Wrapper ..\dist\aws.exe -Alias AWSConsole -DisplayName "AWS Console" -WrapperArgs '-set account=123456789012' -Credential (Get-Credential)`
   Supports `-WhatIf`, `-Force` (overwrite), and `-Remove`.

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
- publish-remoteapp.ps1 refuses `-password`/`{password}` and double quotes in -WrapperArgs.
  Credentials always go via `--use-stdin`. It sets `CommandLineSetting=2`, so only the exact
  registered command line can run. Registry value names follow standard Windows RemoteApp
  behaviour; diff them against one app made by the Publisher the first time it's used.
- pwsh for syntax checks: `/opt/data/tools/bin/pwsh` with `DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1`
  (no libicu). Parser-only; remoting can't run from the container.
- Toolchain on this host: Go at `/opt/data/tools/bin/go` (GOPATH/GOCACHE under
  `/opt/data/tools`), Chrome for Testing at `/opt/data/tools/chrome-linux64/`. Use `Xvfb :97`
  directly because `xvfb-run` fails (no `xauth`).
