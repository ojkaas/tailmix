<#
.SYNOPSIS
Removes tailmix from Windows.

.DESCRIPTION
Stops and deletes the tailmixd service, closes the tray app, and removes the
program files, PATH entry, firewall rule and run-at-login entry. Stopping the
service removes tailmix's network adapter, routes and DNS rules.

.PARAMETER Purge
Also delete %ProgramData%\tailmix, which holds every tailnet's node keys and
login state, and tailmix's registry key. Without it a reinstall keeps your
tailnets logged in.
#>
[CmdletBinding()]
param(
    [switch]$Purge
)

$ErrorActionPreference = 'Stop'

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $arguments = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`"")
    if ($Purge) { $arguments += '-Purge' }
    $process = Start-Process -FilePath 'powershell.exe' -Verb RunAs -ArgumentList $arguments -Wait -PassThru
    exit $process.ExitCode
}

$installDir = Join-Path $env:ProgramFiles 'tailmix'
$serviceName = 'tailmixd'

Get-Process -Name 'tailmix-tray' -ErrorAction SilentlyContinue | Stop-Process -Force
Remove-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'tailmix-tray' -ErrorAction SilentlyContinue

$service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($service) {
    if ($service.Status -ne 'Stopped') {
        Write-Host 'Stopping the tailmixd service...'
        Stop-Service -Name $serviceName -Force
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
    & sc.exe delete $serviceName | Out-Null
}

# tailmixd removes its NRPT (DNS policy) rules when it stops cleanly. Remove
# any that a crash left behind; tailmix lists the rules it owns in its key.
$ownedRules = (Get-ItemProperty -Path 'HKLM:\SOFTWARE\tailmix' -Name 'NRPTRuleIDs' -ErrorAction SilentlyContinue).NRPTRuleIDs
foreach ($rule in @($ownedRules)) {
    if (-not $rule) { continue }
    foreach ($base in @(
            'HKLM:\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig',
            'HKLM:\SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig')) {
        Remove-Item -Recurse -Force -Path (Join-Path $base $rule) -ErrorAction SilentlyContinue
    }
}
Remove-ItemProperty -Path 'HKLM:\SOFTWARE\tailmix' -Name 'NRPTRuleIDs' -ErrorAction SilentlyContinue

Get-NetFirewallRule -DisplayName 'tailmix (tailmixd)' -ErrorAction SilentlyContinue | Remove-NetFirewallRule

$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$entries = $machinePath -split ';' | Where-Object { $_ -and $_ -ne $installDir }
[Environment]::SetEnvironmentVariable('Path', ($entries -join ';'), 'Machine')

if (Test-Path $installDir) {
    # This script may itself live in the install directory; PowerShell has
    # already read it, so removing the folder is safe.
    Remove-Item -Recurse -Force -Path $installDir
}

if ($Purge) {
    Write-Host 'Removing tailmix state...'
    Remove-Item -Recurse -Force -Path (Join-Path $env:ProgramData 'tailmix') -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force -Path 'HKLM:\SOFTWARE\tailmix' -ErrorAction SilentlyContinue
}

Write-Host 'tailmix has been removed.'
if (-not $Purge) {
    Write-Host "Tailnet state was kept in $env:ProgramData\tailmix; rerun with -Purge to delete it."
}
