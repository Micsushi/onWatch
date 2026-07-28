param(
    [string] $RepoDir = "C:\Users\sushi\Documents\Github\onWatch",
    [string] $InstallDir = (Join-Path $env:USERPROFILE ".onwatch"),
    [int] $CheckIntervalSeconds = 3600
)

$ErrorActionPreference = "Stop"

$DevScript = Join-Path $InstallDir "onwatch-dev.ps1"
$InstalledExe = Join-Path $InstallDir "bin\onwatch.exe"
$Launcher = Join-Path $RepoDir "scripts\run-hidden.vbs"
$WScriptPath = Join-Path $env:SystemRoot "System32\wscript.exe"
$PowerShellPath = Join-Path $PSHOME "powershell.exe"
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
    foreach ($path in @($DevScript, $Launcher, $WScriptPath, $PowerShellPath)) {
        if (!(Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Required file not found: $path"
        }
    }
    if (!(Test-Path $RepoDir)) {
        throw "onWatch repo not found: $RepoDir"
    }
    Write-WatchdogLog "starting onWatch from repo"
    & $WScriptPath `
        "//B" `
        "//Nologo" `
        $Launcher `
        $PowerShellPath `
        "-NoLogo" `
        "-NoProfile" `
        "-NonInteractive" `
        "-WindowStyle" `
        "Hidden" `
        "-ExecutionPolicy" `
        "Bypass" `
        "-File" `
        $DevScript `
        "restart"
    if ($LASTEXITCODE -ne 0) {
        throw "onWatch restart failed with exit code $LASTEXITCODE."
    }
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
