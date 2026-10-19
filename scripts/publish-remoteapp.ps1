<#
.SYNOPSIS
    Copy a Safeguard web-app wrapper to an RDS jump host, verify its hash, and publish
    the One Identity RemoteApp Launcher as a RemoteApp that runs it.

.DESCRIPTION
    Mode "Auto" (default) detects which kind of host this is and picks the right store. Publishing
    to the wrong one looks like it worked but the app never appears in the collection, so prefer Auto.

    Mode "Registry" (standalone jump host, no RDS deployment): writes
      HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList\Applications\<Alias>
    over WinRM, like the Remote Application Publisher does.

    Mode "Collection" (full RDS deployment): calls New-/Set-RDRemoteApp against the Connection Broker,
    which writes CentralPublishedResources\PublishedFarms\<farm>\Applications\<Alias> and mirrors
    TSAppAllowList itself. On a deployment host, TSAppAllowList belongs to the broker: hand-writing an
    entry there does not publish anything, so Registry mode refuses to run unless -Force.

    The RDMS cmdlets (Get-RDServer, Get-/New-RDRemoteApp) need a logon session that can authenticate
    onward to the broker. Over WinRM that is a second hop, and they fail with the misleading
    "A Remote Desktop Services deployment does not exist" / "RD Connection Broker server is not
    available". Collection mode therefore runs them from a short-lived SYSTEM scheduled task on the
    target, which has the network identity they need - no CredSSP and no password on disk.

    Always: copies <Wrapper>.exe to -RemoteDir over the PS session, compares SHA256 with the
    local .sha256 file / local hash, and restricts write access on the remote dir to
    Administrators + SYSTEM (Users: read/execute).

    Prints the SPP Access Request Policy values (Application Alias / Display Name) at the end.

.EXAMPLE
    ./publish-remoteapp.ps1 -ComputerName jumprdp.corp.local -Wrapper ../dist/aws.exe `
        -Alias AWSConsole -DisplayName "AWS Console" -WrapperArgs '-set account=123456789012' -Credential (Get-Credential)

.EXAMPLE
    ./publish-remoteapp.ps1 -ComputerName rdsh01 -Mode Collection -ConnectionBroker cb01.corp.local `
        -CollectionName "Safeguard Apps" -Wrapper ../dist/aws.exe -Alias AWSConsole -DisplayName "AWS Console"

.EXAMPLE
    # Credential for the jump host checked out from Safeguard (never shown, never written to disk):
    ./publish-remoteapp.ps1 -ComputerName rds-host.example.com -Wrapper ../dist/azure.exe -Alias AzurePortal `
        -DisplayName "Azure Portal" -SafeguardAppliance spp.example.com `
        -SafeguardAsset rds-host.example.com -SafeguardAccount Administrator -CreateAccessRequest

.EXAMPLE
    # Status only (no checkout, no publish): is the account requestable, is there an open request?
    ./publish-remoteapp.ps1 -ComputerName rds-host.example.com -Alias x -SafeguardAppliance spp.example.com `
        -SafeguardAsset rds-host.example.com -SafeguardAccount Administrator -SafeguardStatusOnly

.EXAMPLE
    ./publish-remoteapp.ps1 -ComputerName jumprdp -Alias AWSConsole -Remove

.NOTES
    Safeguard checkout (-SafeguardAccount) needs the safeguard-ps module (Install-Module safeguard-ps).
    The password goes straight from Get-SafeguardAccessRequestPassword into a SecureString/PSCredential;
    it is never printed, logged, or returned. The request is checked in (or cancelled) on exit unless
    -SafeguardKeepRequest is given, so SPP can rotate the password per policy.
    Exit codes for automation: 0 ok, 10 = account not requestable (no access policy grants you
    Password access), 11 = no open request and -CreateAccessRequest not given, 12 = request pending approval.
#>
[CmdletBinding(SupportsShouldProcess)]
param(
    [Parameter(Mandatory)] [string] $ComputerName,
    [Parameter(Mandatory)] [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9 ._()\-]*$')] [string] $Alias,
    [string] $Wrapper,                       # local path to <site>.exe (not needed with -Remove)
    [string] $DisplayName = $Alias,
    [string] $WrapperArgs = '',              # non-secret wrapper flags, become Launcher --args
    [string] $RemoteDir = 'C:\SafeguardWrappers',
    [string] $LauncherPath = 'C:\Program Files\OneIdentity\OI-SG-RemoteApp-Launcher\OI-SG-RemoteApp-Launcher.exe',
    [switch] $LauncherDebug,                 # adds --enable-debug
    [ValidateSet('Auto','Registry','Collection')] [string] $Mode = 'Auto',
    [string] $ConnectionBroker,
    [string] $CollectionName,
    [string] $IconPath,                      # optional .ico/.exe path on the remote host
    # New-RDRemoteApp insists on extracting an icon and fails the whole publish with
    # "Could not find the specified icon" when the launcher exe has no icon resource.
    # Used only if -IconPath was not given and the iconless attempt fails.
    [string] $FallbackIconPath = 'C:\Windows\System32\imageres.dll',
    [bool]   $ShowInWebAccess = $true,       # Collection mode; match the other apps in the collection
    [pscredential] $Credential,
    [switch] $UseSSL,
    [switch] $Remove,
    [switch] $Force,                         # overwrite an existing app with the same alias
    [switch] $Local,                         # run directly on this host (no PSSession); -ComputerName is informational
    [switch] $SkipAcl,                       # leave the folder ACL alone (shared dir that already holds other apps)
    [string] $ExpectedSha256,                # verify -Wrapper against this hash (use when staged by another tool)

    # ---- Safeguard credential checkout for the WinRM connection (alternative to -Credential) ----
    [string] $SafeguardAppliance,            # SPP host
    [string] $SafeguardAsset,                # SPP asset name/address of the jump host
    [string] $SafeguardAccount,              # SPP account name used for WinRM (local or domain admin)
    [string] $SafeguardLoginName,            # override login form, e.g. 'JUMP01\Administrator' or 'CORP\admin'
    [ValidateSet('Browser','DeviceCode','Certificate','Existing')] [string] $SafeguardAuth = 'Browser',
    [string] $SafeguardCertThumbprint,       # for -SafeguardAuth Certificate (non-interactive)
    [string] $SafeguardIdentityProvider,     # e.g. 'certificate' or an AD/OIDC provider name
    [string] $SafeguardRequestId,            # reuse a specific request
    [switch] $CreateAccessRequest,           # create a Password request if none is open
    [string] $ReasonComment = 'Publish Safeguard RemoteApp wrapper',
    [int]    $ApprovalWaitSeconds = 0,       # wait this long for a pending approval (0 = don't wait)
    [switch] $SafeguardKeepRequest,          # don't check in / cancel at exit
    [switch] $SafeguardStatusOnly,           # report requestability + open request as JSON, then exit
    [switch] $SafeguardInsecure              # skip TLS validation to SPP (lab only)
)
$ErrorActionPreference = 'Stop'

function Build-CommandLine([string]$exe, [string]$wargs, [bool]$dbg) {
    if ($wargs -match '"') { throw 'WrapperArgs must not contain double quotes (they are embedded in --args "...").' }
    if ($wargs -match '(?i)-password\b|\{password\}') { throw 'Refusing to put the password on the command line; wrappers receive it via --use-stdin.' }
    $cl = "--use-stdin --cmd `"$exe`""
    if ($wargs) { $cl += " --args `"$wargs`"" }
    if ($dbg)   { $cl += ' --enable-debug' }
    $cl
}

# =====================================================================================
# Safeguard checkout helpers. The plaintext password exists only inside Get-SgCredential,
# is converted to SecureString immediately, and the plain variable is cleared.
# =====================================================================================
$script:SgReq = $null; $script:SgCreatedReq = $false; $script:SgConnected = $false; $script:SgKeep = $false

function Connect-Sg {
    if (-not (Get-Module -ListAvailable safeguard-ps)) { throw 'safeguard-ps module not installed: Install-Module safeguard-ps -Scope CurrentUser' }
    Import-Module safeguard-ps -ErrorAction Stop
    if ($SafeguardAuth -eq 'Existing') { if (-not $SafeguardSession) { throw 'No existing Safeguard session (run Connect-Safeguard first).' }; return }
    $a = @{ Appliance = $SafeguardAppliance; Insecure = $SafeguardInsecure.IsPresent }
    switch ($SafeguardAuth) {
        'Browser'     { Connect-Safeguard @a -Browser | Out-Null }
        'DeviceCode'  { Connect-Safeguard @a -DeviceCode | Out-Null }
        'Certificate' {
            if (-not $SafeguardCertThumbprint) { throw '-SafeguardCertThumbprint is required for Certificate auth.' }
            $idp = if ($SafeguardIdentityProvider) { $SafeguardIdentityProvider } else { 'certificate' }
            Connect-Safeguard @a -IdentityProvider $idp -Thumbprint $SafeguardCertThumbprint | Out-Null
        }
    }
    $script:SgConnected = $true
}

function Find-SgRequestable {
    # Accounts the signed-in user may request with Password access for this asset.
    $all = @(Find-SafeguardRequestableAccount -Insecure:$SafeguardInsecure -SearchString $SafeguardAccount -AllFields -ErrorAction SilentlyContinue)
    $all | Where-Object {
        $_.AccountName -eq $SafeguardAccount -and
        ($_.AssetName -eq $SafeguardAsset -or $_.NetworkAddress -eq $SafeguardAsset) -and
        $_.AccessRequestType -eq 'Password'
    } | Select-Object -First 1
}

function Find-SgOpenRequest {
    $states = 'RequestAvailable','PasswordCheckedOut','Approved','PendingApproval','PendingAccountRestored','PendingPasswordReset','PendingTimeRequested','New'
    if ($SafeguardRequestId) { return Get-SafeguardAccessRequest -Insecure:$SafeguardInsecure $SafeguardRequestId }
    @(Get-SafeguardMyRequest -Insecure:$SafeguardInsecure -AllFields) | Where-Object {
        $_.AccessRequestType -eq 'Password' -and $_.AccountName -eq $SafeguardAccount -and
        ($_.AssetName -eq $SafeguardAsset -or $_.AssetNetworkAddress -eq $SafeguardAsset) -and $states -contains $_.State
    } | Sort-Object CreatedOn -Descending | Select-Object -First 1
}

function Get-SgStatus {
    $r = Find-SgRequestable; $o = Find-SgOpenRequest
    [pscustomobject]@{
        Appliance = $SafeguardAppliance; Asset = $SafeguardAsset; Account = $SafeguardAccount
        Requestable = [bool]$r
        AccountDomainName = $(if ($r) { $r.AccountDomainName })
        OpenRequestId = $(if ($o) { $o.Id }); OpenRequestState = $(if ($o) { $o.State })
    }
}

function Get-SgCredential {
    $st = Get-SgStatus
    if (-not $st.OpenRequestId) {
        if (-not $st.Requestable) {
            Write-Error "No access policy lets you request Password access to '$SafeguardAccount' on '$SafeguardAsset'. Create one (see references/safeguard-config.md, 'jump host credential policy')."
            exit 10
        }
        if (-not $CreateAccessRequest) {
            Write-Error "No open access request for '$SafeguardAccount' on '$SafeguardAsset'. Re-run with -CreateAccessRequest to create one."
            exit 11
        }
        $script:SgReq = New-SafeguardAccessRequest -Insecure:$SafeguardInsecure $SafeguardAsset $SafeguardAccount Password -ReasonComment $ReasonComment
        $script:SgCreatedReq = $true
        Write-Host "Created access request $($script:SgReq.Id) (state: $($script:SgReq.State))"
    } else {
        $script:SgReq = Get-SafeguardAccessRequest -Insecure:$SafeguardInsecure $st.OpenRequestId
        Write-Host "Using existing access request $($script:SgReq.Id) (state: $($script:SgReq.State))"
    }
    $deadline = (Get-Date).AddSeconds($ApprovalWaitSeconds)
    while ($script:SgReq.State -in 'PendingApproval','New','PendingAccountRestored','PendingPasswordReset','PendingTimeRequested') {
        if ((Get-Date) -ge $deadline) { Write-Error "Request $($script:SgReq.Id) is $($script:SgReq.State). Approve it in SPP, then re-run (it will be reused)."; $script:SgKeep = $true; exit 12 }
        Start-Sleep 5; $script:SgReq = Get-SafeguardAccessRequest -Insecure:$SafeguardInsecure $script:SgReq.Id
    }
    # Local account -> '<host>\name' (works with an IP for NTLM over HTTPS/TrustedHosts); domain -> 'DOMAIN\name'.
    $login = if ($SafeguardLoginName) { $SafeguardLoginName }
             elseif ($script:SgReq.AccountDomainName) { "$($script:SgReq.AccountDomainName)\$($script:SgReq.AccountName)" }
             else { "$ComputerName\$SafeguardAccount" }
    $plain = Get-SafeguardAccessRequestPassword -Insecure:$SafeguardInsecure $script:SgReq.Id
    try   { $sec = ConvertTo-SecureString -String $plain -AsPlainText -Force }
    finally { $plain = $null; Remove-Variable plain -ErrorAction SilentlyContinue; [GC]::Collect() }
    Write-Host "Checked out credential for $login from request $($script:SgReq.Id) (value not displayed)."
    New-Object System.Management.Automation.PSCredential($login, $sec)
}

function Close-Sg {
    if ($script:SgReq -and -not $SafeguardKeepRequest -and -not $script:SgKeep) {
        try { Close-SafeguardAccessRequest -Insecure:$SafeguardInsecure $script:SgReq.Id | Out-Null; Write-Host "Checked in / closed request $($script:SgReq.Id)." }
        catch { Write-Warning "Could not close request $($script:SgReq.Id): $_" }
    }
    if ($script:SgConnected) { try { Disconnect-Safeguard | Out-Null } catch {} }
}

if ($SafeguardAccount) {
    foreach ($n in 'SafeguardAppliance','SafeguardAsset') { if (-not (Get-Variable $n -ValueOnly) -and $SafeguardAuth -ne 'Existing') { throw "-$n is required with -SafeguardAccount." } }
    if ($Credential) { throw 'Use either -Credential or -SafeguardAccount, not both.' }
    Connect-Sg
    if ($SafeguardStatusOnly) { Get-SgStatus | ConvertTo-Json; Close-Sg; return }
    try { $Credential = Get-SgCredential } catch { Close-Sg; throw }
}

$sessArgs = @{ ComputerName = $ComputerName }
if ($Credential) { $sessArgs.Credential = $Credential }
if ($UseSSL)     { $sessArgs.UseSSL = $true }
$s = $null
if (-not $Local) {
    try { $s = New-PSSession @sessArgs } catch { if ($SafeguardAccount) { Close-Sg }; throw }
}

# Runs a script block on the target: through the PSSession, or in-process with -Local.
# -Local exists so the script can be staged onto the jump host and run natively there,
# which avoids the WinRM double hop (and lets non-Windows admin boxes drive it).
function Invoke-Target {
    param([object[]] $ArgumentList, [scriptblock] $ScriptBlock)
    if ($Local) { Invoke-Command -ArgumentList $ArgumentList -ScriptBlock $ScriptBlock }
    else { Invoke-Command -Session $s -ArgumentList $ArgumentList -ScriptBlock $ScriptBlock }
}

# Runs a script block on the target as SYSTEM, via a short-lived scheduled task.
# The RDMS cmdlets need a logon session that can authenticate to the Connection Broker; a WinRM
# session cannot (second hop). SYSTEM authenticates as the computer account, which the broker
# accepts, so this avoids CredSSP and never puts a password on disk.
function Invoke-TargetAsSystem {
    param([object[]] $ArgumentList = @(), [scriptblock] $ScriptBlock)
    $payload = [Management.Automation.PSSerializer]::Serialize(@($ArgumentList))
    $out = Invoke-Target -ArgumentList $ScriptBlock.ToString(), $payload -ScriptBlock {
        param($body, $payload)
        $tag    = 'sgPublish_' + [guid]::NewGuid().ToString('N')
        $dir    = Join-Path $env:SystemRoot 'Temp'
        $script = Join-Path $dir "$tag.ps1"
        $result = Join-Path $dir "$tag.xml"
        # Single-quoted here-strings: nothing in $body or $payload is expanded or re-parsed here.
        $inner = @"
`$ErrorActionPreference = 'Stop'
try {
    `$a  = [Management.Automation.PSSerializer]::Deserialize(@'
$payload
'@)
    `$sb = [scriptblock]::Create(@'
$body
'@)
    `$r = & `$sb @a
    [Management.Automation.PSSerializer]::Serialize(@{ ok = `$true; data = `$r }) | Set-Content -Path '$result' -Encoding UTF8
} catch {
    [Management.Automation.PSSerializer]::Serialize(@{ ok = `$false; err = `$_.Exception.Message }) | Set-Content -Path '$result' -Encoding UTF8
}
"@
        Set-Content -Path $script -Value $inner -Encoding UTF8
        try {
            $act = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$script`""
            $pri = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
            Register-ScheduledTask -TaskName $tag -Action $act -Principal $pri -Force | Out-Null
            Start-ScheduledTask -TaskName $tag
            $n = 0
            while ((Get-ScheduledTask -TaskName $tag).State -ne 'Ready' -and $n -lt 300) { Start-Sleep -Milliseconds 500; $n++ }
            $n = 0
            while (-not (Test-Path $result) -and $n -lt 20) { Start-Sleep -Milliseconds 500; $n++ }
            if (-not (Test-Path $result)) { throw "SYSTEM task $tag produced no result (timed out)." }
            Get-Content -Path $result -Raw
        } finally {
            Unregister-ScheduledTask -TaskName $tag -Confirm:$false -ErrorAction SilentlyContinue
            Remove-Item $script, $result -Force -ErrorAction SilentlyContinue
        }
    }
    $r = [Management.Automation.PSSerializer]::Deserialize($out)
    if (-not $r.ok) { throw "On $ComputerName (as SYSTEM): $($r.err)" }
    $r.data
}

# What kind of RemoteApp host is this? Read-only, and readable over plain WinRM:
# deliberately avoids the RDMS cmdlets, which misreport "deployment does not exist" on a second hop.
function Get-RdsDeployment {
    Invoke-Target -ArgumentList @() -ScriptBlock {
        $farms = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\CentralPublishedResources\PublishedFarms'
        $names = if (Test-Path $farms) { @(Get-ChildItem $farms | Select-Object -ExpandProperty PSChildName) } else { @() }
        $coll = $null
        try { $coll = Get-CimInstance -Namespace root\cimv2\rdms -ClassName Win32_RDSHCollection -ErrorAction Stop | Select-Object -First 1 } catch { }
        $broker = $null
        try { $broker = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Terminal Server\ClusterSettings' -ErrorAction Stop).SessionDirectoryLocation } catch { }
        [pscustomobject]@{
            Farms          = $names
            CollectionName = if ($coll) { $coll.Name } else { $null }
            Broker         = $broker
        }
    }
}
try {
    # ---------- decide which store owns RemoteApps on this host ----------
    $dep = Get-RdsDeployment
    $hasDeployment = $dep.Farms.Count -gt 0 -or $dep.CollectionName
    if ($Mode -eq 'Auto') {
        $Mode = if ($hasDeployment) { 'Collection' } else { 'Registry' }
        Write-Host "Detected mode: $Mode$(if ($hasDeployment) { " (collection '$($dep.CollectionName)', broker '$($dep.Broker)')" })"
    }
    if ($Mode -eq 'Registry' -and $hasDeployment -and -not $Force) {
        throw ("$ComputerName has an RDS deployment (collection '$($dep.CollectionName)'), where the broker owns " +
               'TSAppAllowList. A hand-written entry there is silently ignored and never appears in the collection. ' +
               'Use -Mode Collection (or -Force to override).')
    }
    if ($Mode -eq 'Collection') {
        if (-not $CollectionName) { $CollectionName = $dep.CollectionName }
        if (-not $ConnectionBroker) { $ConnectionBroker = $dep.Broker }
        if (-not $CollectionName -or -not $ConnectionBroker) {
            throw 'Collection mode needs -ConnectionBroker and -CollectionName (could not detect them).'
        }
    }

    # ---------- remove ----------
    if ($Remove) {
        if ($PSCmdlet.ShouldProcess("$ComputerName/$Alias", 'Remove RemoteApp')) {
            if ($Mode -eq 'Collection') {
                Invoke-TargetAsSystem -ArgumentList $Alias,$ConnectionBroker,$CollectionName -ScriptBlock {
                    param($Alias,$CB,$Coll)
                    Import-Module RemoteDesktop
                    Remove-RDRemoteApp -ConnectionBroker $CB -CollectionName $Coll -Alias $Alias -Force
                    "Removed '$Alias' from collection '$Coll'"
                }
            } else {
                Invoke-Target -ArgumentList $Alias -ScriptBlock {
                    param($Alias)
                    $k = "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList\Applications\$Alias"
                    if (Test-Path $k) { Remove-Item $k -Recurse -Force; "Removed $k" } else { "Not present: $k" }
                }
            }
        }
        return
    }

    if (-not $Wrapper) { throw '-Wrapper is required unless -Remove is used.' }
    $Wrapper = (Resolve-Path $Wrapper).Path
    $exeName = Split-Path $Wrapper -Leaf
    $remoteExe = Join-Path $RemoteDir $exeName
    $localHash = (Get-FileHash $Wrapper -Algorithm SHA256).Hash
    $shaFile = "$Wrapper.sha256"
    if (Test-Path $shaFile) {
        $expected = ((Get-Content $shaFile -Raw).Trim() -split '\s+')[0]
        if ($expected -ne $localHash) { throw "Local $exeName does not match $shaFile (expected $expected, got $localHash)." }
    }
    if ($ExpectedSha256 -and $ExpectedSha256 -replace '\s','' -ne $localHash) {
        throw "$exeName does not match -ExpectedSha256 (expected $ExpectedSha256, got $localHash)."
    }
    $cmdLine = Build-CommandLine $remoteExe $WrapperArgs $LauncherDebug.IsPresent

    # ---------- preflight ----------
    $pre = Invoke-Target -ArgumentList $LauncherPath,$Alias,$Mode -ScriptBlock {
        param($L,$Alias,$Mode)
        $key = if ($Mode -eq 'Collection') {
            # The collection's own store; TSAppAllowList is only the broker's mirror of it.
            $farms = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\CentralPublishedResources\PublishedFarms'
            @(Get-ChildItem $farms -ErrorAction SilentlyContinue |
                ForEach-Object { Join-Path $_.PSPath "Applications\$Alias" } |
                Where-Object { Test-Path $_ }).Count -gt 0
        } else {
            Test-Path "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList\Applications\$Alias"
        }
        [pscustomobject]@{
            Launcher = Test-Path $L
            Chrome   = (Test-Path 'C:\Program Files\Google\Chrome\Application\chrome.exe') -or (Test-Path 'C:\Program Files (x86)\Google\Chrome\Application\chrome.exe')
            Edge     = (Test-Path 'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe') -or (Test-Path 'C:\Program Files\Microsoft\Edge\Application\msedge.exe')
            Exists   = $key
        }
    }
    if (-not $pre.Launcher) { throw "OI-SG-RemoteApp-Launcher not found on $ComputerName at '$LauncherPath' (use -LauncherPath)." }
    if (-not ($pre.Chrome -or $pre.Edge)) { Write-Warning "Neither Chrome nor Edge found on $ComputerName." }
    if ($pre.Exists -and -not $Force) { throw "RemoteApp alias '$Alias' already exists on $ComputerName. Use -Force to overwrite." }

    # ---------- copy + verify ----------
    if ($PSCmdlet.ShouldProcess("${ComputerName}:$remoteExe", 'Copy wrapper')) {
        Invoke-Target -ArgumentList $RemoteDir,$SkipAcl.IsPresent -ScriptBlock {
            param($d,$skipAcl)
            if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d | Out-Null }
            # Admins/SYSTEM full, Users read+execute, no inheritance: users must not be able to swap the exe.
            # -SkipAcl leaves a shared directory (one that already holds other published apps) untouched.
            if (-not $skipAcl) {
                & icacls $d /inheritance:r /grant:r '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-545:(OI)(CI)RX' | Out-Null
            }
        }
        if ($Local) {
            if ((Resolve-Path $Wrapper).Path -ne $remoteExe) { Copy-Item -Path $Wrapper -Destination $remoteExe -Force }
        } else {
            Copy-Item -Path $Wrapper -Destination $remoteExe -ToSession $s -Force
        }
        $remoteHash = Invoke-Target -ArgumentList $remoteExe -ScriptBlock { param($p) (Get-FileHash $p -Algorithm SHA256).Hash }
        if ($remoteHash -ne $localHash) { throw "Hash mismatch after copy: local $localHash remote $remoteHash" }
        Write-Host "Copied and verified $remoteExe ($localHash)"
    }

    # ---------- publish ----------
    if ($PSCmdlet.ShouldProcess("$ComputerName/$Alias", "Publish RemoteApp ($Mode)")) {
        if ($Mode -eq 'Collection') {
            $created = Invoke-TargetAsSystem -ArgumentList $Alias,$DisplayName,$LauncherPath,$cmdLine,$IconPath,$FallbackIconPath,$ConnectionBroker,$CollectionName,$ShowInWebAccess -ScriptBlock {
                param($Alias,$Name,$Launcher,$CmdLine,$Icon,$FallbackIcon,$CB,$Coll,$Web)
                Import-Module RemoteDesktop
                $p = @{ ConnectionBroker=$CB; CollectionName=$Coll; DisplayName=$Name; FilePath=$Launcher;
                        CommandLineSetting='Require'; RequiredCommandLine=$CmdLine; ShowInWebAccess=$Web }
                if ($Icon) { $p.IconPath = $Icon; $p.IconIndex = 0 }
                if (Get-RDRemoteApp -ConnectionBroker $CB -CollectionName $Coll -Alias $Alias -ErrorAction SilentlyContinue) {
                    Set-RDRemoteApp -Alias $Alias @p
                } else {
                    try {
                        # Always pass -Alias: letting RDS generate one yields names like
                        # "OISGRemoteAppLauncher(1)" that differ from an existing
                        # "OISGRemoteAppLauncher (1)" only by a space.
                        New-RDRemoteApp -Alias $Alias @p | Out-Null
                    } catch {
                        if (-not $Icon -and $FallbackIcon -and $_.Exception.Message -match 'icon') {
                            # The launcher exe carries no icon resource; New-RDRemoteApp treats that as fatal.
                            New-RDRemoteApp -Alias $Alias @p -IconPath $FallbackIcon -IconIndex 0 | Out-Null
                        } else { throw }
                    }
                }
                Get-RDRemoteApp -ConnectionBroker $CB -CollectionName $Coll -Alias $Alias |
                    Select-Object -First 1 -Property Alias,DisplayName,RequiredCommandLine
            }
            if (-not $created) { throw "Publish reported success but '$Alias' is not in collection '$CollectionName'." }
            if ($created.Alias -ne $Alias) { throw "Broker stored alias '$($created.Alias)', not the requested '$Alias'." }
            if ($created.RequiredCommandLine -ne $cmdLine) {
                throw "Broker stored command line '$($created.RequiredCommandLine)', not '$cmdLine'."
            }
        } else {
            Invoke-Target -ArgumentList $Alias,$DisplayName,$LauncherPath,$cmdLine,$IconPath -ScriptBlock {
                param($Alias,$Name,$Launcher,$CmdLine,$Icon)
                $base = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList'
                # Allow RemoteApps on a non-broker host (same as Publisher / RemoteApp Manager).
                if (-not (Test-Path $base)) { New-Item $base -Force | Out-Null }
                New-ItemProperty $base -Name fDisabledAllowList -Value 0 -PropertyType DWord -Force | Out-Null
                $k = New-Item -Path "$base\Applications\$Alias" -Force
                # ShortPath mirrors what the Remote Application Publisher writes for its own entries.
                $short = try { (New-Object -ComObject Scripting.FileSystemObject).GetFile($Launcher).ShortPath } catch { $Launcher }
                $vals = [ordered]@{
                    Name                = @($Name,'String')
                    Path                = @($Launcher,'String')
                    VPath               = @($Launcher,'String')
                    ShortPath           = @($short,'String')
                    CommandLineSetting  = @(2,'DWord')        # 2 = require exactly RequiredCommandLine
                    RequiredCommandLine = @($CmdLine,'String')
                    ShowInTSWA          = @(0,'DWord')
                    SecurityDescriptor  = @('','String')
                }
                if ($Icon) {
                    $vals.IconPath  = @($Icon,'String')
                    $vals.IconIndex = @(0,'DWord')
                }
                foreach ($n in $vals.Keys) {
                    New-ItemProperty -Path $k.PSPath -Name $n -Value $vals[$n][0] -PropertyType $vals[$n][1] -Force | Out-Null
                }
            }
        }
    }

    Write-Host ""
    Write-Host "Published '$DisplayName' on $ComputerName ($Mode)."
    Write-Host "  Launcher command line : $cmdLine"
    Write-Host "SPP Access Request Policy -> Security tab:"
    Write-Host "  Application Display Name : $DisplayName"
    Write-Host "  Application Alias        : ||$Alias"
}
finally {
    if ($s) { Remove-PSSession $s -ErrorAction SilentlyContinue }
    if ($SafeguardAccount) { Close-Sg }
}
