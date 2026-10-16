# Launching a RemoteApp from the SPP GUI (SCALUS)

Publishing the RemoteApp and the access policy only covers the server side. How the
session actually starts depends on the client, and a working policy can still fail to
launch.

Two routes exist from the SPP web GUI:

- **Download the RDP file**, then open it. SPP writes a complete `.rdp`, so this works
  wherever the OS RDP client understands RemoteApp.
- **Start RDP Session**, which hands an `rdp://` URL to
  [SCALUS](https://github.com/OneIdentity/SCALUS), the local URL-scheme handler.

## What SPP sends

For a RemoteDesktopApplication policy, `POST /AccessRequests/{id}/InitializeSession`
returns an `RdpConnectionFile` containing:

```
remoteapplicationmode:i:1
alternate shell:s:||OISGRemoteAppLauncher (3)
remoteapplicationprogram:s:||OISGRemoteAppLauncher (3)
remoteapplicationname:s:Azure Portal
full address:s:<session appliance>
username:s:account~<account>%asset~<asset>%vaultaddress~<spp>%token~<token>%<app host account>%<app host>
```

The GUI turns these into `rdp://full%20address=s:...&username=s:...&...`, percent-encoding
`%` as `%25`. The session token is in the username; the password field is unused.

## The trap: the handler can drop the RemoteApp silently

SCALUS parses the URL into tokens and fills a template. `scalus launch -p -u "<url>"`
prints the tokens and the generated file without launching — use it before blaming the
policy. It correctly extracts:

```
AlternateShell            : ||OISGRemoteAppLauncher (3)
Remoteapplicationprogram  : ||OISGRemoteAppLauncher (3)
Remoteapplicationname     : Azure Portal
```

But the stock Linux handler is `remmina-rdp`, whose `rdp.remmina` template has **no field
for any of them**. The RemoteApp is dropped, Remmina asks the appliance for a full desktop,
and the session fails — while the downloaded `.rdp` works, because it keeps the fields.

This is not fixable by editing the Remmina template: Remmina's RDP plugin has no
RemoteApplication support and hardcodes `remoteapplicationmode:i:0`.

The symptom looks policy-specific but is not. It hits **every** RemoteApp policy on that
client equally.

## Fix on Linux: FreeRDP

FreeRDP 3 supports RemoteApp via `/app:program:...`. `scripts/scalus/sg-freerdp.sh` wraps it
so one handler serves both session types — ordinary desktop sessions arrive with the
RemoteApp tokens empty, and FreeRDP must not then be passed an empty `/app:`.

```sh
cp scripts/scalus/sg-freerdp.sh ~/.SCALUS/ && chmod +x ~/.SCALUS/sg-freerdp.sh
```

Add to `~/.SCALUS/SCALUS.json` under `applications`, and point the `rdp` protocol at it:

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

Verify without connecting:

```sh
scalus verify
scalus launch -p -u "<rdp url>"                 # handler + arguments
SG_FREERDP_DRYRUN=1 ~/.SCALUS/sg-freerdp.sh HOST USER "||Alias (3)" "Name"
```

Note this replaces Remmina as the handler for *all* `rdp://` URLs, including plain desktop
sessions. Keep the backup SCALUS writes nothing of — copy `SCALUS.json` yourself first.

## On Windows

SCALUS ships `RdpRemoteAppTemplate.rdp` and `WinRdpTemplate.rdp`, both of which already
reference `%Remoteapplicationprogram%`, `%Remoteapplicationname%`, `%AlternateShell%` and
`%Remoteapplicationcmdline%`. Use one of those with `mstsc` rather than the plain default
template. `WinRdpTemplate.rdp` is the better default: its `%AlternateShell?1:0%`
conditionals make it degrade correctly for non-RemoteApp sessions.
