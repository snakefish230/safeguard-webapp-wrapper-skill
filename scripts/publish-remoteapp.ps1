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
    ./publish-remoteapp.ps1 -ComputerName jumprdp -Alias AWSConsole -Remove
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
    [switch] $Force                          # overwrite an existing app with the same alias
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

$sessArgs = @{ ComputerName = $ComputerName }
if ($Credential) { $sessArgs.Credential = $Credential }
if ($UseSSL)     { $sessArgs.UseSSL = $true }
$s = New-PSSession @sessArgs
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
finally { Remove-PSSession $s -ErrorAction SilentlyContinue }
