$ErrorActionPreference = "Stop"

$scriptsDirectory = Split-Path -Parent $PSScriptRoot
$launcherPath = Join-Path $scriptsDirectory "run-hidden.vbs"
$registrationPath = Join-Path $scriptsDirectory "register-windows-startup.ps1"
$watchdogPath = Join-Path $scriptsDirectory "windows-background-watchdog.ps1"

if (-not (Test-Path -LiteralPath $launcherPath -PathType Leaf)) {
    throw "Windows background services must include a windowless launcher."
}

$registration = Get-Content -LiteralPath $registrationPath -Raw
if ($registration -notmatch "wscript\.exe") {
    throw "The watchdog task must start from wscript instead of a console executable."
}
if ($registration -match "New-ScheduledTaskAction\s+-Execute\s+['""]?powershell") {
    throw "The watchdog task must not execute PowerShell directly."
}

$watchdog = Get-Content -LiteralPath $watchdogPath -Raw
if ($watchdog -notmatch "run-hidden\.vbs") {
    throw "Watchdog restarts must also use the windowless launcher."
}

Write-Output "Windows background tests passed."
