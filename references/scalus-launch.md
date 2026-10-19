# Launching a RemoteApp from the SPP GUI

Publishing the RemoteApp and the access policy only covers the server side. A correct,
working policy can still fail to launch, and the two routes out of the SPP GUI do not
behave identically:

- **Download the RDP file**, then open it.
- **Start RDP Session**, which hands an `rdp://` URL to
  [SCALUS](https://github.com/OneIdentity/SCALUS), the local URL-scheme handler.

If one route works and the other does not, the fault is client-side. Do not start by
re-checking the policy — compare the two generated `.rdp` files instead.

## The 255-character username limit

This is the trap most likely to bite, and it is silent.

SPP packs the entire session context into the RDP `username` field:

```
<apphost account>\account~<account>%asset~<asset>%vaultaddress~<spp>%token~<~90 chars>%<apphost account>%<apphost>
```

Per [MS-RDPBCGR], `TS_INFO_PACKET.cbUserName` is capped at **512 bytes including the null
terminator** — 256 UTF-16 characters, so **255 usable**. Exceed it and `mstsc` refuses to
connect with:

```
Error code: 0x904
Extended error code: 0x7
"This computer can't connect to the remote computer."
```

The error names the network, so it sends you hunting for firewall, DNS or gateway faults.
It is none of those. Nothing is logged server-side, because the client never connects.

**The `rdp://` URL route is ~29 characters more expensive than the downloaded file**: it
repeats the application-host account as a `domain\` prefix *in addition to* its usual
position near the end. So a request can sit under the limit as a downloaded `.rdp` and
over it via Start RDP Session — which is exactly what "the download works but the GUI
button doesn't" means.

Measure before theorising. Pull the usernames out of the SCALUS log and count:

```sh
grep 'Parsing URL' scalus.log | python3 -c '
import sys,urllib.parse,re
for l in sys.stdin:
    u=l.split("Parsing URL",1)[1].rsplit(" as an rdp URL",1)[0]
    for p in u.split("&"):
        k,_,v=p.partition("=")
        if urllib.parse.unquote(k).strip()=="username":
            d=urllib.parse.unquote(v)[2:]
            print(len(d), re.sub(r"token~[A-Za-z0-9]+","token~<redacted>",d)[:90])'
```

Anything at 256 or above fails; working entries sit well below. Comparing a working app
against a failing one on the same appliance isolates it immediately.

### Shortening the username

Only some components are yours to control. In order of least disruption:

| Component | Occurrences | Notes |
|---|---|---|
| `asset~<name>` | 1 | **The asset name — usually the right lever.** Renaming is safe: policies reference the asset by ID, and the string is rebuilt per session. |
| apphost account | **2** | Every character saved counts twice, but it is a real directory account. |
| `account~<name>` | 1 | Usually a real UPN; rarely changeable. |
| `vaultaddress~` | 1 | Fixed. |
| `token~` | 1 | ~86–90 chars, appliance-generated. Budget for ~92. |

Removing only a space is not enough — aim for several characters of margin, since token
length varies between sessions. Prefer a short asset name such as `Azure` over
`Azure portal` (saves 7) when the account UPN is already long.

A long cloud-account UPN plus a descriptive asset name is the combination that overflows,
so cloud-targeted RemoteApps are the ones to check first.

## Linux: Remmina cannot launch a RemoteApp at all

A separate issue, and Linux-only — irrelevant if you launch from Windows.

SCALUS's `DefaultRdpUrlParser` correctly extracts `AlternateShell`,
`Remoteapplicationprogram`, `Remoteapplicationname` and `Remoteapplicationcmdline`, but
the stock Linux handler fills `rdp.remmina`, which has no field for any of them. The
alias is silently dropped and the appliance is asked for a full desktop. Patching the
template does not help: Remmina's RDP plugin hardcodes `remoteapplicationmode:i:0`.

Use FreeRDP 3 instead. `scripts/scalus/sg-freerdp.sh` wraps it so one handler serves both
session types — ordinary desktop sessions arrive with the RemoteApp tokens empty, and
FreeRDP must not then receive an empty `/app:`.

```sh
cp scripts/scalus/sg-freerdp.sh ~/.SCALUS/ && chmod +x ~/.SCALUS/sg-freerdp.sh
```

Register it in `~/.SCALUS/SCALUS.json` and point the `rdp` protocol at it:

```json
{
  "id": "safeguard-freerdp",
  "name": "Safeguard FreeRDP (desktop and RemoteApp)",
  "platforms": ["Linux"],
  "protocol": "rdp",
  "parser": { "parserId": "rdp", "options": ["waitforexit"], "useDefaultTemplate": false },
  "exec": "/home/<you>/.SCALUS/sg-freerdp.sh",
  "args": ["%Host%", "%User%", "%Remoteapplicationprogram%", "%Remoteapplicationname%"]
}
```

This replaces Remmina for *all* `rdp://` URLs, including plain desktops. Back up
`SCALUS.json` first.

## Windows: check which template is in use

SCALUS ships `RdpRemoteAppTemplate.rdp` and `WinRdpTemplate.rdp`, both carrying
`remoteapplicationmode:i:1` and the `%Remoteapplicationprogram%` /
`%Remoteapplicationname%` placeholders.

The `exampleRdpTemplate.rdp` that some installs default to sets
**`remoteapplicationmode:i:0`** while still substituting `%AlternateShell%`. Sessions
launched that way still start the aliased application, so they look like they work — but
as a desktop-framed window rather than a seamless RemoteApp. If RemoteApps launch but
look wrong, check this line before anything else.

`WinRdpTemplate.rdp` is the better default: its `%AlternateShell?1:0%` conditionals let it
degrade correctly for non-RemoteApp sessions.

## Diagnosing

```sh
scalus verify
scalus launch -p -u "<rdp url>"     # resolved tokens + generated file, without connecting
```

`launch -p` is the key tool: it shows exactly what the handler receives and produces. The
SCALUS log also records the template used, the signing step and the final command line.

Confirm the log is actually recording your attempts before drawing conclusions from it. A
stale log, or one from a different machine than the user is testing on, will happily
support a wrong theory.
