param(
    [string] $TaskName = "onWatch Personal Fork Watchdog",
    [string] $RepoDir = "C:\Users\sushi\Documents\Github\onWatch",
    [int] $CheckIntervalSeconds = 3600
)

$ErrorActionPreference = "Stop"

$WatchdogScript = Join-Path $RepoDir "scripts\windows-background-watchdog.ps1"
if (!(Test-Path $WatchdogScript)) {
    throw "Watchdog script not found: $WatchdogScript"
}

$escapedScript = $WatchdogScript.Replace('"', '\"')
$escapedRepo = $RepoDir.Replace('"', '\"')
$arguments = "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$escapedScript`" -RepoDir `"$escapedRepo`" -CheckIntervalSeconds $CheckIntervalSeconds"

$action = New-ScheduledTaskAction -Execute "powershell.exe" -Argument $arguments
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit (New-TimeSpan -Days 3650) `
    -MultipleInstances IgnoreNew `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -StartWhenAvailable
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType Interactive -RunLevel Limited

Register-ScheduledTask `
    -TaskName $TaskName `
    -Action $action `
    -Trigger $trigger `
    -Settings $settings `
    -Principal $principal `
    -Description "Starts and monitors the local onWatch personal fork in the background." `
    -Force | Out-Null

Start-ScheduledTask -TaskName $TaskName
Write-Host "Registered and started scheduled task: $TaskName"
