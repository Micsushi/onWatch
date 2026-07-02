$ErrorActionPreference = "Stop"

$files = git ls-files "*.go"
if (-not $files) {
    exit 0
}

$unformatted = gofmt -l @files
if ($unformatted) {
    Write-Output "gofmt needed for:"
    $unformatted | ForEach-Object { Write-Output $_ }
    exit 1
}
