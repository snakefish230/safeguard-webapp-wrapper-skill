# ADUC (Active Directory Users and Computers) — native, not a browser

Added 2026-11-17. `wrapper/sites/aduc` → `dist/aduc.exe`.

Starts `%SystemRoot%\System32\mmc.exe dsa.msc` with
`CreateProcessWithLogonW(LOGON_NETCREDENTIALS_ONLY)`, which is the same as `runas /netonly`.
The console runs under the RDS user's own token, and LDAP/Kerberos to the DCs uses the vaulted
account. The password goes from the Launcher's stdin pipe straight into the API. The wrapper
waits for MMC to exit, then exits itself.

## Launcher line
```
--use-stdin --cmd "C:\OneIdentity\aduc.exe" --args ""
```
Optional `--args`: `-server {asset}` (pin a DC), `-domain corp.example.com`,
`-msc dssite.msc|domain.msc|gpmc.msc|<path>` (other consoles), `-set logon_domain=CORP`,
and `-debug` (also keeps the console window visible).

## Requirements on the jump host
- RSAT AD DS tools (`dsa.msc`), plus the **Secondary Logon** service (`seclogon`) running.
  Without it, `CreateProcessWithLogonW` fails.
- The SPP account must be a **directory (AD) account**, so `Target.AccountDomainName` is
  sent. Otherwise use `DOMAIN\user`/UPN as the account name or `-set logon_domain=`.
- The host need not be domain-joined if `-domain`/`-server` is given and DNS resolves the DCs.

## Pitfalls
- `mmc.exe` has a `highestAvailable` manifest. For an admin RDS user, CreateProcess then
  fails with 740 (elevation required). The wrapper sets `__COMPAT_LAYER=RunAsInvoker`.
- Netonly credentials aren't checked at launch. A wrong password shows up later, as ADUC
  "access denied" / "domain could not be found", not as a wrapper error.
- `whoami` inside the session shows the RDS user. That's expected. Check the DC security log
  (4624/4769) for the vaulted account.

## SPP policy (verified on pcaob-poc, policy Id 9)
Type RemoteDesktopApplication, alias `||ADUC`, app host = Apps (10.0.0.7) logging in as the RDS
service account. Scope = **DC asset only**, `SessionAccessAccountType=PolicySpecific`,
`SessionAccessAccounts=[SGPOV-admin2]`. An AD account added as an Account scope item gives
requesters nothing; see safeguard-config.md Step 4.
