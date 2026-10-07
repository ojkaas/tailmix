<#
.SYNOPSIS
Installs or upgrades tailmix on Windows.

.DESCRIPTION
Copies tailmixd, the tailmix CLI, the tray app and wintun.dll to
"Program Files\tailmix", registers the tailmixd Windows service, adds the CLI
to the system PATH, allows tailmixd through Windows Defender Firewall, and
starts the tray app for the current user.

Run it from the extracted release folder. It asks for elevation if needed.
Rerunning it upgrades an existing installation in place; state in
%ProgramData%\tailmix is kept.

.PARAMETER NoTray
Do not start the tray app or register it to run at login.
#>
[CmdletBinding()]
param(
    [switch]$NoTray
)

$ErrorActionPreference = 'Stop'

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $arguments = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`"")
    if ($NoTray) { $arguments += '-NoTray' }
    $process = Start-Process -FilePath 'powershell.exe' -Verb RunAs -ArgumentList $arguments -Wait -PassThru
    exit $process.ExitCode
}

$source = $PSScriptRoot
$installDir = Join-Path $env:ProgramFiles 'tailmix'
$serviceName = 'tailmixd'
$files = @('tailmixd.exe', 'tailmix.exe', 'tailmix-tray.exe', 'wintun.dll')

foreach ($file in $files) {
    if (-not (Test-Path (Join-Path $source $file))) {
        throw "Missing $file next to install.ps1; run the script from the extracted release folder."
    }
}

Write-Host 'Stopping running tailmix components...'
$service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($service -and $service.Status -ne 'Stopped') {
    Stop-Service -Name $serviceName -Force
    $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
}
Get-Process -Name 'tailmix-tray' -ErrorAction SilentlyContinue | Stop-Process -Force

Write-Host "Installing to $installDir..."
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
foreach ($file in $files + @('install.ps1', 'uninstall.ps1', 'README.md', 'LICENSE.txt', 'wintun-LICENSE.txt', 'THIRD-PARTY-LICENSES.md')) {
    $path = Join-Path $source $file
    if (Test-Path $path) {
        Copy-Item -Force -Path $path -Destination $installDir
    }
}

$daemon = Join-Path $installDir 'tailmixd.exe'
$binPath = "`"$daemon`""
if (Get-Service -Name $serviceName -ErrorAction SilentlyContinue) {
    & sc.exe config $serviceName binPath= $binPath start= auto | Out-Null
} else {
    New-Service -Name $serviceName -BinaryPathName $binPath -DisplayName 'tailmix' -StartupType Automatic | Out-Null
}
& sc.exe description $serviceName 'Connects this computer to multiple Tailscale tailnets at the same time.' | Out-Null
# Restart after crashes: 5s, 10s, then every 30s; reset the count after a day.
& sc.exe failure $serviceName reset= 86400 actions= restart/5000/restart/10000/restart/30000 | Out-Null
if ($LASTEXITCODE -ne 0) { throw "sc.exe failure returned $LASTEXITCODE" }

# Direct (peer-to-peer) WireGuard connections need inbound UDP to tailmixd.
# Without the rule tailmix still works, relayed through DERP.
$ruleName = 'tailmix (tailmixd)'
Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -DisplayName $ruleName -Direction Inbound -Action Allow -Protocol UDP `
    -Program $daemon -Profile Any -Description 'Allows direct WireGuard connections to tailmix.' | Out-Null

$machinePath = [Environment]::GetEnvironmentVariable('Path', 'Machine')
if (-not (($machinePath -split ';') -contains $installDir)) {
    [Environment]::SetEnvironmentVariable('Path', ($machinePath.TrimEnd(';') + ';' + $installDir), 'Machine')
    Write-Host "Added $installDir to the system PATH (open a new terminal to use 'tailmix')."
}

Write-Host 'Starting the tailmixd service...'
Start-Service -Name $serviceName
(Get-Service -Name $serviceName).WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
$deadline = (Get-Date).AddSeconds(30)
while (-not ([IO.Directory]::GetFiles('\\.\pipe\') -contains '\\.\pipe\tailmix\tailmixd.sock')) {
    if ((Get-Date) -gt $deadline) {
        throw "tailmixd did not open its control pipe; see $env:ProgramData\tailmix\tailmixd.log"
    }
    Start-Sleep -Milliseconds 500
}

if (-not $NoTray) {
    $tray = Join-Path $installDir 'tailmix-tray.exe'
    Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'tailmix-tray' -Value "`"$tray`""
    # Launch through Explorer so the tray runs unelevated, as the signed-in user.
    Start-Process -FilePath 'explorer.exe' -ArgumentList "`"$tray`""
}

Write-Host ''
Write-Host 'tailmix is installed and running.'
Write-Host 'Add a tailnet from the tray icon (Add tailnet...), or from a new terminal:'
Write-Host '    tailmix profiles add work'
Write-Host '    tailmix ts --profile work up'
