# Diagnosing a wrapper that misbehaves

Most of the time lost building these wrappers is lost to guessing. The failures
here are not subtle once you have the evidence, and they are nearly impossible
to reason out without it. Get the log first.

## Rule zero: ask what the failure actually looked like

Before forming any theory, get the **exact error text or a screenshot**. Two
rounds were wasted on this project theorising about the wrong layer entirely;
one screenshot showing `0x904` reframed the problem in seconds, because it
proved the failure happened *before* the wrapper ever ran.

The corollary: before reasoning from a log, confirm the log is recording the
user's attempts. An afternoon went into analysing a SCALUS log that contained
nothing but the investigator's own test runs, on a machine the user was not
using.

## The wrapper's own log

`core` writes one file per wrapper per day, as the account the RemoteApp runs
as — the **application host account** from the SPP policy, not the user who
requested the session:

```
C:\Users\<apphost-account>\AppData\Roaming\OneIdentity\
  OI-SG-RemoteApp-Launcher-Orchestration\<site>_<YYYY-MM-DD>.log
```

At the default level it records only the start, the navigation, and any step
that failed. That is enough to see *where* a run died but not *why*.

### Turning on -debug

`-debug` goes in the wrapper's own arguments, which live in the **RemoteApp
registration on the RDS host** — not in the SPP policy. A policy's
`ApplicationCmdLine` is normally empty; SPP only supplies the alias.

For a standalone host (no RDS deployment), that is the registry:

```powershell
$k = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\' +
     'TSAppAllowList\Applications\<Alias>'
(Get-ItemProperty $k).RequiredCommandLine            # look first
Set-ItemProperty $k -Name RequiredCommandLine -Value `
  '--use-stdin --cmd "C:\OI_Custom\<site>.exe" --args "-incognito -debug"'
```

On a Connection Broker deployment use `Set-RDRemoteApp -CommandLineSetting
Require -RequiredCommandLine '...'` instead. `Get-RDRemoteApp` fails with *"A
Remote Desktop Services deployment does not exist"* on a standalone host — that
error means "use the registry", not "something is broken".

To list what is published and with which arguments:

```powershell
Get-ChildItem 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList\Applications' |
  ForEach-Object { $p = Get-ItemProperty $_.PSPath
    "{0} | {1} | {2}" -f $_.PSChildName, $p.Name, $p.RequiredCommandLine }
```

`-debug` never logs secrets. Values are not written at all: only key *names*,
and a length when a typed value failed to read back. The e2e suite asserts that
neither the password nor an OTP code appears in the log.

Leave it on while diagnosing, then take it back out — the files are per-day and
otherwise grow unattended.

### Reading a step log

Each step logs `step do=...` on entry and `step ok step=N` on exit, where **N is
the 1-based position in the site's `steps` slice**. Count the entries in
`wrapper/sites/<site>/main.go` to map a number to a line. A step number that
jumps when you add or remove a step is why failures should be quoted with the
step *name* as well.

The fragment that solved the hardest bug on this project:

```
15:42:40 "form did not advance on click, pressing Enter"  field=loginfmt
15:43:00 "form still showing after Enter"                 field=loginfmt
15:43:00 "already on target page, skipping optional click" skip=passwd
15:43:00 secret input[name="passwd"] -> ok in 73ms
```

Read together, those lines say the wrapper believed it was stuck on the username
page while simultaneously typing into the password field. That contradiction is
the whole diagnosis: the page had advanced and the check was wrong.

## The Launcher's own log

`OI-SG-RemoteApp-Launcher.exe` takes `--enable-debug`, which is separate from the
wrapper's `-debug`. Add it when you need to see what the Launcher handed to the
wrapper — for example when a `Target.*` key is missing and you need to know
whether SPP never sent it or the wrapper failed to read it.

## The SCALUS log

"Start RDP Session" in the SPP GUI hands the `rdp://` URL to
[SCALUS](https://github.com/OneIdentity/SCALUS) on the operator's workstation.
Its log shows the URL it received, the template it filled in, and the command it
ran. On Windows the path is configured in `SCALUS.json`; the default is beside
the binary. Check the timestamps before trusting it, and remember it lives on
whichever machine the operator clicked from.

## Symptom → cause

| Symptom | Where | Cause |
|---|---|---|
| `0x904`, extended `0x7`, "This computer can't connect to the remote computer" — downloaded `.rdp` works, "Start RDP Session" does not | `mstsc`, before any connection | The RDP username SPP builds exceeds 255 characters. The `rdp://` URL is ~29 chars longer than the `.rdp` file. See `scalus-launch.md`. |
| `step failed ... OTP field never appeared` | wrapper log | Usually not the OTP step at all — an earlier step is silently burning its timeout. Check the timestamps between steps before touching OTP selectors. |
| `no unused TOTP code valid for >= 10s` | wrapper log | The flow took longer than the codes live. SPP mints them at session start with ~30s lives, so anything over about 90s total exhausts them. Find the step that is waiting, do not raise the retry count. |
| Form sits until the user presses Enter | reported by the user | A success check that waits for the field to *disappear*. Providers that park the previous view never deliver that. Wait for a positive signal instead. |
| `context canceled` on every step from a point onward | wrapper log | The browser was closed — usually the user giving up. Look at what stalled immediately before, not at the cancelled steps. |
| Step log shows a click succeeding but nothing happening | wrapper log with `-debug` | Either a genuinely lost click, or — far more likely — a wrong success check. Rule out the check first; it costs nothing. |

## Method

Three habits, each of which would have saved a round on this project:

1. **Measure before theorising.** "Azure usernames are 256 chars, Dropbox's are
   232" ended a debate that two plausible theories had not.
2. **A theory that does not explain why the *working* cases work is wrong.**
   Parentheses in the alias looked compelling until `(1)` and `(2)` were noticed
   working fine.
3. **Make the test reproduce the failure before believing the fix.** See
   `testing.md`.
