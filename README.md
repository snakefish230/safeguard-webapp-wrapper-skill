# Safeguard Web App Wrapper Skill

Builds Windows executables (Go + chromedp) that One Identity Safeguard's RemoteApp Launcher
runs on an RDS jump host to open a browser, navigate to a web console, and inject vaulted
credentials (incl. TOTP). The password is passed to the wrapper on stdin by the Launcher and
never reaches a command line, a log, or the screen.

This repository is a Copilot skill: `SKILL.md` is the agent workflow, and `references/` holds
the detail it loads on demand.

```bash
scripts/build.sh aws         # -> dist/aws.exe (+ .sha256)
scripts/build.sh all
tests/e2e/run.sh             # real-Chrome E2E against a mock login
tests/e2e/run-azure-otp.sh   # the Entra/TOTP flow, including its failure modes
```

Go >= 1.22 and a Chrome or Chromium are the only prerequisites. If either lives somewhere the
scripts won't find, set `GO_BIN_DIR` and `CHROME`.

Publishing to a jump host needs a privileged credential; check it out of Safeguard rather than
pasting it. From Windows use `scripts/publish-remoteapp.ps1` (authenticates in-process via
safeguard-ps). From Linux use `scripts/sg_winrm.py` with a token from `scripts/sg_token.py`.

| Reference | Covers |
|---|---|
| `references/launcher-contract.md` | the stdin JSON and flags the Launcher passes |
| `references/deployment.md` | publishing the exe and registering the RemoteApp |
| `references/safeguard-config.md` | the SPP asset, account, policy and entitlement |
| `references/scalus-launch.md` | "Start RDP Session" / SCALUS, and the 255-char username cap |
| `references/diagnosing.md` | **start here when something fails** — logs, `-debug`, symptom → cause |
| `references/testing.md` | how to write a mock that can actually catch a bug |
| `references/sites/` | per-site selectors and the quirks found on each login |

Based on https://github.com/OneIdentity/SafeguardAutomation/tree/master/RDP%20Applications/Go%20chromedp
