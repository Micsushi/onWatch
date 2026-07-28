param(
    [string] $TaskName = "onWatch Personal Fork Watchdog",
    [string] $RepoDir = "C:\Users\sushi\Documents\Github\onWatch",
    [int] $CheckIntervalSeconds = 3600
)

$ErrorActionPreference = "Stop"

$WatchdogScript = Join-Path $RepoDir "scripts\windows-background-watchdog.ps1"
$LauncherScript = Join-Path $RepoDir "scripts\run-hidden.vbs"
$WScriptPath = Join-Path $env:SystemRoot "System32\wscript.exe"
$PowerShellPath = Join-Path $PSHOME "powershell.exe"
foreach ($path in @($WatchdogScript, $LauncherScript, $WScriptPath, $PowerShellPath)) {
    if (!(Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required file not found: $path"
    }
}

$argumentList = @(
    "//B",
    "//Nologo",
    $LauncherScript,
    $PowerShellPath,
    "-NoLogo",
    "-NoProfile",
    "-NonInteractive",
    "-WindowStyle",
    "Hidden",
    "-ExecutionPolicy",
    "Bypass",
    "-File",
    $WatchdogScript,
    "-RepoDir",
    $RepoDir,
    "-CheckIntervalSeconds",
    [string]$CheckIntervalSeconds
)
$arguments = ($argumentList | ForEach-Object {
    '"' + $_.Replace('"', '\"') + '"'
}) -join " "

$existingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($existingTask -and $existingTask.State -eq "Running") {
    Stop-ScheduledTask -InputObject $existingTask
}

$action = New-ScheduledTaskAction -Execute $WScriptPath -Argument $arguments
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
