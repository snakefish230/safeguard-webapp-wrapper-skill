# Configuring Safeguard (SPP) for a web-app wrapper via the Safeguard MCP

Requires the **Safeguard MCP** server (https://github.com/OneIdentity/safeguard-mcp), stdio,
pinned version, one entry per appliance, for example:
```yaml
mcp_servers:
  safeguard_lab:
    command: npx
    args: ["-y", "@oneidentity/safeguard-mcp@0.3.1"]
    env: { SAFEGUARD_HOST: spp.example.com }
```
Tools: `Safeguard_Connect` (device-code sign-in on first use), `Safeguard_Discover`,
`Safeguard_Schema`, `Safeguard_Execute`, `Safeguard_Reference`. The signed-in user needs
**Asset Admin** (assets/accounts) and **Security Policy Admin** (entitlements/policies).

The schema below was extracted from the MCP's bundled `scripts/Core.json` (SPP v4 API). Still
call `Safeguard_Schema` before the first POST against a given appliance, because field sets
vary by version.

## Objects involved
| Object | Purpose | API |
|---|---|---|
| **Target asset + account** | the web app credential to inject (e.g. AWS IAM user) | `/v4/Assets`, `/v4/AssetAccounts` |
| **Jump host asset + account** | Windows RDS host and the account that logs in to it via RDP | same |
| **SPS connection policy** | the RDP connection policy on the joined SPS | `GET /v4/Cluster/SessionModules/ConnectionPolicies` |
| **Entitlement** (= Role) | who may request | `/v4/Roles`, `/v4/Roles/{id}/Members/Add` |
| **Access request policy** | RDP Application session config | `/v4/AccessPolicies`, `/v4/AccessPolicies/{id}/ScopeItems/Add` |

## Step 0 — prerequisites and lookups (read-only)
```
GET /v4/Me                                                      # who am I / admin roles
GET /v4/Cluster/SessionModules/ConnectionPolicies               # need >=1, else SPS not joined (stop)
GET /v4/Platforms?filter=DisplayName contains 'Other'&fields=Id,DisplayName,PlatformFamily
GET /v4/Platforms?filter=DisplayName contains 'Windows Server'&fields=Id,DisplayName
```

## Step 1 — target asset/account (the web app)
Ask: *"Is there already a Safeguard asset and account for <site>?"*
- Yes → look them up:
  `GET /v4/Assets?filter=Name contains '<x>'&fields=Id,Name,NetworkAddress,PlatformDisplayName`
  `GET /v4/AssetAccounts?filter=Asset.Id eq <id>&fields=Id,Name,HasPassword,HasTotpAuthenticator`
  and confirm the right one with the user.
- No → offer to create (confirm the body with the user first):
  ```
  POST /v4/Assets  {"Name":"AWS Console - Prod","NetworkAddress":"123456789012",
                    "PlatformId":<Other Managed / Other id>,"Description":"Web app via RDP application"}
  POST /v4/AssetAccounts {"Asset":{"Id":<assetId>},"Name":"<iam-user>","Description":"..."}
  ```
  NetworkAddress conventions: AWS = account id/alias (wrapper flag `-account-from-asset`);
  appliance consoles = host FQDN (wrapper `-url https://{asset}/...`).
  Platform choice: use a real connector/custom platform if password rotation is wanted;
  otherwise "Other Managed" / "Other" (unmanaged, stored only).
- **Password / TOTP secret: never handled by the agent.** Tell the user to set them in the SPP
  UI (Account → Set Password; Account → TOTP Authenticator). Then verify with
  `GET /v4/AssetAccounts/{id}?fields=HasPassword,HasTotpAuthenticator`.

## Step 2 — jump host asset/account
Ask: *"Is the RDS jump host already an asset with an account that can start RemoteApps?"*
- Yes → look up as above (`PlatformDisplayName contains 'Windows'`).
- No → offer:
  ```
  POST /v4/Assets {"Name":"jumprdp","NetworkAddress":"jumprdp.corp.local","PlatformId":<Windows Server id>}
  POST /v4/AssetAccounts {"Asset":{"Id":<id>},"Name":"svc_rdpapp"}   # or a directory account
  ```
  Same rule: the user sets the password in the UI (or it's a managed account SPP already rotates).

## Step 3 — entitlement (Role)
Ask: *"New entitlement, or add to an existing one?"*
- Existing: `GET /v4/Roles?fields=Id,Name,PolicyCount,UserCount` → user chooses.
  Check the name is unique: `GET /v4/Roles/{id}/CheckUniquePolicyName?name=<policy name>`.
- New:
  ```
  POST /v4/Roles {"Name":"AWS Console Access","Description":"RDP app access to AWS console"}
  POST /v4/Roles/{roleId}/Members/Add  [{"Id":<userOrGroupId>}]
  ```
  Look up members with `GET /v4/Users?filter=Name eq '<x>'` or `GET /v4/UserGroups?filter=Name eq '<x>'`.

## Step 4 — access request policy (RDP Application)
```
POST /v4/AccessPolicies
{
  "Name": "AWS Console - RDP App",
  "RoleId": <roleId>,
  "AccessRequestProperties": {
    "AccessRequestType": "RemoteDesktopApplication",
    "SessionAccessAccountType": "None"
  },
  "SessionProperties": {
    "SessionModuleConnectionId": <from ConnectionPolicies>,
    "SessionConnectionPolicyRef": "<from ConnectionPolicies>",
    "RemoteDesktopApplicationProperties": {
      "ApplicationHostAssetId": <jumpHostAssetId>,
      "ApplicationHostAccountId": <jumpHostAccountId>,
      "ApplicationHostUserSupplied": false,
      "ApplicationDisplayName": "AWS Console",
      "ApplicationAlias": "||AWSConsole"
    }
  },
  "RequesterProperties": {"DefaultReleaseDurationHours": 2, "MaximumReleaseDurationHours": 8,
                          "AllowCustomDuration": true, "RequireReasonComment": true},
  "ApproverProperties": {"RequireApproval": false}
}
→ policyId
POST /v4/AccessPolicies/{policyId}/ScopeItems/Add
  [{"ScopeItemType":"Account","Id":<targetAccountId>}]
```
- `ApplicationDisplayName` / `ApplicationAlias` must match the RemoteApp published on the
  jump host (`publish-remoteapp.ps1` prints both; alias = `||<Alias>`).
- `SessionAccessAccountType` controls the account used for the RDP login to the jump host.
  `None` uses the policy's ApplicationHostAccount. `LinkedAccount` uses the requester's
  linked account, and `UserSupplied` prompts the requester.
- `ApplicationProgram` / `ApplicationCmdLine` aren't needed when the Launcher is published
  with a required command line; leave them unset unless the appliance version requires them.
- Approvals: if wanted, set `"RequireApproval": true` and add `ApproverSets`
  (`[{"RequiredApprovers":1,"Approvers":[{"Id":<userId>}]}]`).
- If the SessionConnectionPolicyRef field shape differs on this appliance, read an existing
  RDP-app policy (`GET /v4/AccessPolicies?filter=AccessRequestProperties.AccessRequestType eq 'RemoteDesktopApplication'`)
  and mirror it.

## Step 5 — verify
```
GET /v4/AccessPolicies/{policyId}   # InvalidConnectionPolicy must be false
GET /v4/Me/RequestEntitlements?...  # (as a member) the account should be requestable as RDP Application
```
End-to-end test: request it in the SPP portal → Launch / Download RDP → browser opens signed in.

## Jump host credential policy (for publish-remoteapp.ps1 checkout)
Only needed when the user can't already request Password access to the RDS admin account.
```
GET  /v4/Assets?filter=NetworkAddress eq 'rds-host.example.com' or Name eq '<name>'&fields=Id,Name
GET  /v4/AssetAccounts?filter=Asset.Id eq <id> and Name eq '<acct>'&fields=Id,Name,HasPassword
GET  /v4/Me?fields=Id,Name                                    # requester to add as member
POST /v4/Roles {"Name":"RDS Jump Host Admin","Description":"Checkout for RemoteApp publishing"}
POST /v4/Roles/{roleId}/Members/Add [{"Id":<userId>}]
POST /v4/AccessPolicies {
  "Name":"RDS Jump Host Admin - Password", "RoleId":<roleId>,
  "AccessRequestProperties":{"AccessRequestType":"Password","ChangePasswordAfterCheckin":true},
  "RequesterProperties":{"DefaultReleaseDurationHours":1,"MaximumReleaseDurationHours":4,"RequireReasonComment":true},
  "ApproverProperties":{"RequireApproval":false}
}
POST /v4/AccessPolicies/{policyId}/ScopeItems/Add [{"ScopeItemType":"Account","Id":<accountId>}]
```
`ChangePasswordAfterCheckin:true` rotates the admin password after the script checks it in, if
the asset's platform can rotate it. If the user is already in an entitlement that should hold
this, add a Password policy to that role instead. Ask which they prefer.

## Rollback (if the user wants to undo)
Delete in reverse order: `DELETE /v4/AccessPolicies/{id}`, then the role if it was newly created
(`DELETE /v4/Roles/{id}`), then any newly created accounts/assets. Track what was *created*
in this run so you never delete pre-existing objects.
