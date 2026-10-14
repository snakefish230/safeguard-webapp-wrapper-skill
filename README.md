# Safeguard Web App Wrapper Skill

Builds Windows executables (Go + chromedp) that One Identity Safeguard's RemoteApp Launcher
runs on an RDS jump host to open a browser, navigate to a web console, and inject vaulted
credentials (incl. TOTP). See `SKILL.md` for the agent workflow.

```bash
scripts/build.sh aws        # -> dist/aws.exe
scripts/build.sh all
tests/e2e/run.sh            # real-Chrome E2E against a mock login
```
Based on https://github.com/OneIdentity/SafeguardAutomation/tree/master/RDP%20Applications/Go%20chromedp
