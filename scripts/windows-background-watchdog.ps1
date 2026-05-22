param(
    [string] $RepoDir = "C:\Users\sushi\Documents\Github\onWatch",
    [string] $InstallDir = (Join-Path $env:USERPROFILE ".onwatch"),
    [int] $CheckIntervalSeconds = 3600
)

$ErrorActionPreference = "Stop"

$Wrapper = Join-Path $InstallDir "onwatch.cmd"
$InstalledExe = Join-Path $InstallDir "bin\onwatch.exe"
$LogDir = Join-Path $InstallDir "logs"
$LogPath = Join-Path $LogDir "watchdog.log"
$LockPath = Join-Path $InstallDir "onwatch-watchdog.lock"

function Write-WatchdogLog {
    param([string] $Message)
    New-Item -ItemType Directory -Force $LogDir | Out-Null
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    Add-Content -Path $LogPath -Value "$timestamp $Message"
}

function Test-OnWatchRunning {
    if (!(Test-Path $InstalledExe)) {
        return $false
    }
    $processes = Get-Process -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -and ($_.Path -ieq $InstalledExe) }
    return @($processes).Count -gt 0
}

function Start-OnWatchFromRepo {
    if (!(Test-Path $Wrapper)) {
        throw "onWatch wrapper not found: $Wrapper"
    }
    if (!(Test-Path $RepoDir)) {
        throw "onWatch repo not found: $RepoDir"
    }
    Write-WatchdogLog "starting onWatch from repo"
    Start-Process -FilePath $Wrapper -ArgumentList "restart" -WorkingDirectory $RepoDir -WindowStyle Hidden | Out-Null
}

New-Item -ItemType Directory -Force $InstallDir | Out-Null
if (Test-Path $LockPath) {
    $existingPid = Get-Content $LockPath -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($existingPid -match '^\d+$') {
        $existing = Get-Process -Id ([int] $existingPid) -ErrorAction SilentlyContinue
        if ($existing) {
            Write-WatchdogLog "another watchdog is already running: pid $existingPid"
            exit 0
        }
    }
}

$PID | Set-Content -Path $LockPath
Write-WatchdogLog "watchdog started: pid $PID"

try {
    while ($true) {
        try {
            if (!(Test-OnWatchRunning)) {
                Start-OnWatchFromRepo
            }
        } catch {
            Write-WatchdogLog "error: $($_.Exception.Message)"
        }
        Start-Sleep -Seconds $CheckIntervalSeconds
    }
} finally {
    Remove-Item $LockPath -Force -ErrorAction SilentlyContinue
    Write-WatchdogLog "watchdog stopped: pid $PID"
}
