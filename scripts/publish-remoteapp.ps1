<#
.SYNOPSIS
    Copy a Safeguard web-app wrapper to an RDS jump host, verify its hash, and publish
    the One Identity RemoteApp Launcher as a RemoteApp that runs it.

.DESCRIPTION
    Mode "Registry" (default; standalone jump host): writes
      HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList\Applications\<Alias>
    over WinRM, like the Remote Application Publisher does.
    Mode "Collection" (full RDS deployment): calls New-/Set-RDRemoteApp against the Connection Broker
    from the jump host session (RemoteDesktop module required there).

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
    ./publish-remoteapp.ps1 -ComputerName 10.0.7.103 -Wrapper ../dist/azure.exe -Alias AzurePortal `
        -DisplayName "Azure Portal" -SafeguardAppliance spp.safeguardlab.net `
        -SafeguardAsset 10.0.7.103 -SafeguardAccount Administrator -CreateAccessRequest

.EXAMPLE
    # Status only (no checkout, no publish): is the account requestable, is there an open request?
    ./publish-remoteapp.ps1 -ComputerName 10.0.7.103 -Alias x -SafeguardAppliance spp.safeguardlab.net `
        -SafeguardAsset 10.0.7.103 -SafeguardAccount Administrator -SafeguardStatusOnly

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
    [Parameter(Mandatory)] [ValidatePattern('^[A-Za-z0-9_\-]+$')] [string] $Alias,
    [string] $Wrapper,                       # local path to <site>.exe (not needed with -Remove)
    [string] $DisplayName = $Alias,
    [string] $WrapperArgs = '',              # non-secret wrapper flags, become Launcher --args
    [string] $RemoteDir = 'C:\SafeguardWrappers',
    [string] $LauncherPath = 'C:\Program Files\OneIdentity\OI-SG-RemoteApp-Launcher\OI-SG-RemoteApp-Launcher.exe',
    [switch] $LauncherDebug,                 # adds --enable-debug
    [ValidateSet('Registry','Collection')] [string] $Mode = 'Registry',
    [string] $ConnectionBroker,
    [string] $CollectionName,
    [string] $IconPath,                      # optional .ico/.exe path on the remote host
    [pscredential] $Credential,
    [switch] $UseSSL,
    [switch] $Remove,
    [switch] $Force,                         # overwrite an existing app with the same alias

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
            Connect-Safeguard @a -IdentityProvider ($SafeguardIdentityProvider ? $SafeguardIdentityProvider : 'certificate') -Thumbprint $SafeguardCertThumbprint | Out-Null
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
try { $s = New-PSSession @sessArgs } catch { if ($SafeguardAccount) { Close-Sg }; throw }
try {
    # ---------- remove ----------
    if ($Remove) {
        if ($PSCmdlet.ShouldProcess("$ComputerName/$Alias", 'Remove RemoteApp')) {
            Invoke-Command -Session $s -ArgumentList $Mode,$Alias,$ConnectionBroker,$CollectionName -ScriptBlock {
                param($Mode,$Alias,$CB,$Coll)
                if ($Mode -eq 'Collection') {
                    Import-Module RemoteDesktop
                    Remove-RDRemoteApp -ConnectionBroker $CB -CollectionName $Coll -Alias $Alias -Force
                } else {
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
    if ($Mode -eq 'Collection' -and (-not $ConnectionBroker -or -not $CollectionName)) {
        throw 'Collection mode needs -ConnectionBroker and -CollectionName.'
    }
    $cmdLine = Build-CommandLine $remoteExe $WrapperArgs $LauncherDebug.IsPresent

    # ---------- preflight ----------
    $pre = Invoke-Command -Session $s -ArgumentList $LauncherPath,$Alias,$Mode -ScriptBlock {
        param($L,$Alias,$Mode)
        [pscustomobject]@{
            Launcher = Test-Path $L
            Chrome   = (Test-Path 'C:\Program Files\Google\Chrome\Application\chrome.exe') -or (Test-Path 'C:\Program Files (x86)\Google\Chrome\Application\chrome.exe')
            Edge     = (Test-Path 'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe') -or (Test-Path 'C:\Program Files\Microsoft\Edge\Application\msedge.exe')
            Exists   = Test-Path "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList\Applications\$Alias"
        }
    }
    if (-not $pre.Launcher) { throw "OI-SG-RemoteApp-Launcher not found on $ComputerName at '$LauncherPath' (use -LauncherPath)." }
    if (-not ($pre.Chrome -or $pre.Edge)) { Write-Warning "Neither Chrome nor Edge found on $ComputerName." }
    if ($Mode -eq 'Registry' -and $pre.Exists -and -not $Force) { throw "RemoteApp alias '$Alias' already exists on $ComputerName. Use -Force to overwrite." }

    # ---------- copy + verify ----------
    if ($PSCmdlet.ShouldProcess("${ComputerName}:$remoteExe", 'Copy wrapper')) {
        Invoke-Command -Session $s -ArgumentList $RemoteDir -ScriptBlock {
            param($d)
            if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d | Out-Null }
            # Admins/SYSTEM full, Users read+execute, no inheritance: users must not be able to swap the exe.
            & icacls $d /inheritance:r /grant:r '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-545:(OI)(CI)RX' | Out-Null
        }
        Copy-Item -Path $Wrapper -Destination $remoteExe -ToSession $s -Force
        $remoteHash = Invoke-Command -Session $s -ArgumentList $remoteExe -ScriptBlock { param($p) (Get-FileHash $p -Algorithm SHA256).Hash }
        if ($remoteHash -ne $localHash) { throw "Hash mismatch after copy: local $localHash remote $remoteHash" }
        Write-Host "Copied and verified $remoteExe ($localHash)"
    }

    # ---------- publish ----------
    if ($PSCmdlet.ShouldProcess("$ComputerName/$Alias", "Publish RemoteApp ($Mode)")) {
        Invoke-Command -Session $s -ArgumentList $Mode,$Alias,$DisplayName,$LauncherPath,$cmdLine,$IconPath,$ConnectionBroker,$CollectionName -ScriptBlock {
            param($Mode,$Alias,$Name,$Launcher,$CmdLine,$Icon,$CB,$Coll)
            if ($Mode -eq 'Collection') {
                Import-Module RemoteDesktop
                $p = @{ ConnectionBroker=$CB; CollectionName=$Coll; Alias=$Alias; DisplayName=$Name; FilePath=$Launcher;
                        CommandLineSetting='Require'; RequiredCommandLine=$CmdLine; ShowInWebAccess=$false }
                if ($Icon) { $p.IconPath = $Icon; $p.IconIndex = 0 }
                if (Get-RDRemoteApp -ConnectionBroker $CB -CollectionName $Coll -Alias $Alias -ErrorAction SilentlyContinue) {
                    $p.Remove('Alias') | Out-Null; Set-RDRemoteApp -Alias $Alias @p
                } else { New-RDRemoteApp @p | Out-Null }
            } else {
                $base = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList'
                # Allow RemoteApps on a non-broker host (same as Publisher / RemoteApp Manager).
                if (-not (Test-Path $base)) { New-Item $base -Force | Out-Null }
                New-ItemProperty $base -Name fDisabledAllowList -Value 0 -PropertyType DWord -Force | Out-Null
                $k = New-Item -Path "$base\Applications\$Alias" -Force
                $vals = [ordered]@{
                    Name                = @($Name,'String')
                    Path                = @($Launcher,'String')
                    VPath               = @($Launcher,'String')
                    CommandLineSetting  = @(2,'DWord')        # 2 = require exactly RequiredCommandLine
                    RequiredCommandLine = @($CmdLine,'String')
                    ShowInTSWA          = @(0,'DWord')
                    IconPath            = @($(if ($Icon) { $Icon } else { $Launcher }),'String')
                    IconIndex           = @(0,'DWord')
                    SecurityDescriptor  = @('','String')
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
    Remove-PSSession $s -ErrorAction SilentlyContinue
    if ($SafeguardAccount) { Close-Sg }
}
