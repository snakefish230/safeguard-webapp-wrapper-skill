# OI-SG-RemoteApp-Launcher ↔ wrapper contract

## Components
SPP (policy/approval/vault) → SPS (RDP proxy, recording, credential injection) → RDS jump
host running the published **Launcher** → Launcher runs `--cmd` (our wrapper) → wrapper drives
the browser.

## Launcher parameters
| Param | Meaning |
|---|---|
| `--cmd <path>` | program to start (wrapper exe) |
| `--args "<string>"` | arguments. Safeguard tokens such as `{username}` `{password}` `{asset}` `{Target.X}` are substituted in legacy mode |
| `--use-stdin` | (≥3.0.1) pass all values as one JSON line on the wrapper's stdin; `--args` arrives as `cli_args` |
| `--enable-debug` | Launcher logs to `%AppData%\OneIdentity\OI-SG-RemoteApp-Launcher\` |

## stdin JSON (observed keys)
`username`, `password`, `asset`, `cli_args`, `Target.AssetNetworkAddress`, `Target.AssetName`,
`Target.AccountName`, `Target.AccountDomainName`, `Target.TotpCodes` (array of
`{Code, UnixTime, Period}`). core aliases `asset` and `username` from the Target.* keys when
they're absent. Lookups are case-insensitive.

## Wrapper flags provided by core (all wrappers)
`-debug -browser chrome|edge -edge -incognito -kiosk -insecure -user-data-dir <dir>
-step-timeout 90s -no-wait -set key=value (repeatable) -asset` plus legacy `-username/-account
-password`. Values supplied by Safeguard win over `-set`.

Logs: `%AppData%\OneIdentity\OI-SG-RemoteApp-Launcher-Orchestration\<wrapper>_<date>.log`
(key names only, never values).
