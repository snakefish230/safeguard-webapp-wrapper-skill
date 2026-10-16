$ErrorActionPreference = 'SilentlyContinue'

Write-Output "=== C:\OI_Custom ==="
if (Test-Path 'C:\OI_Custom') {
    Get-ChildItem 'C:\OI_Custom' -File | Select-Object Name, Length, LastWriteTime |
        Format-Table -Auto | Out-String
} else { Write-Output 'MISSING' }

Write-Output "=== Launcher ==="
$cands = @(
    'C:\Program Files\OneIdentity\OI-SG-RemoteApp-Launcher\OI-SG-RemoteApp-Launcher.exe',
    'C:\Program Files (x86)\OneIdentity\OI-SG-RemoteApp-Launcher\OI-SG-RemoteApp-Launcher.exe'
)
$found = $cands | Where-Object { Test-Path $_ }
if (-not $found) {
    $found = Get-ChildItem 'C:\Program Files','C:\Program Files (x86)','C:\OI_Custom' -Recurse -Filter 'OI-SG-RemoteApp-Launcher.exe' -Depth 4 |
        Select-Object -ExpandProperty FullName
}
if ($found) { $found | ForEach-Object { Write-Output $_ } } else { Write-Output 'NOT FOUND' }

Write-Output "=== Browsers ==="
@('C:\Program Files\Google\Chrome\Application\chrome.exe',
  'C:\Program Files (x86)\Google\Chrome\Application\chrome.exe',
  'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe') |
    ForEach-Object { if (Test-Path $_) { Write-Output "present: $_" } }

Write-Output "=== RDS roles ==="
(Get-WindowsFeature RDS-RD-Server, RDS-Connection-Broker |
    Select-Object Name, InstallState | Format-Table -Auto | Out-String)

Write-Output "=== RD deployment ==="
Import-Module RemoteDesktop -ErrorAction SilentlyContinue
if (Get-Module RemoteDesktop) {
    Write-Output "RemoteDesktop module: loaded"
    $srv = Get-RDServer -ErrorAction SilentlyContinue
    if ($srv) { $srv | Select-Object Server, Roles | Format-Table -Auto | Out-String }
    else { Write-Output "Get-RDServer: no deployment reachable ($($Error[0].Exception.Message))" }
    $col = Get-RDSessionCollection -ErrorAction SilentlyContinue
    if ($col) { $col | Select-Object CollectionName, CollectionDescription | Format-Table -Auto | Out-String }
    else { Write-Output "Get-RDSessionCollection: none ($($Error[0].Exception.Message))" }
} else { Write-Output 'RemoteDesktop module NOT available' }

Write-Output "=== Registry TSAppAllowList apps ==="
$base = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Terminal Server\TSAppAllowList'
if (Test-Path $base) {
    (Get-ItemProperty $base | Select-Object fDisabledAllowList | Format-List | Out-String)
    Get-ChildItem "$base\Applications" | ForEach-Object {
        $p = Get-ItemProperty $_.PSPath
        [pscustomobject]@{
            Alias = $_.PSChildName; Name = $p.Name; Path = $p.Path
            CmdLineSetting = $p.CommandLineSetting; CmdLine = $p.RequiredCommandLine
        }
    } | Format-List | Out-String
} else { Write-Output 'no TSAppAllowList' }
