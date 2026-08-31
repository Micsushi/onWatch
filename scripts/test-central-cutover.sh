#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
fixture=""
while [[ $# -gt 0 ]]; do
  case "$1" in --fixture) fixture="${2:-}"; shift 2;; *) echo "unknown option: $1" >&2; exit 2;; esac
done
[[ -n "$fixture" && -d "$fixture" ]] || { echo "--fixture directory is required" >&2; exit 2; }

temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT
binary="$temp_dir/onwatch"
CGO_ENABLED=0 go build -o "$binary" "$root"
source_db="$temp_dir/source.db"
archive="$temp_dir/history.onwatch.zip"
central_db="$temp_dir/central.db"

if [[ -f "$fixture/source.db" ]]; then
  cp "$fixture/source.db" "$source_db"
fi
go test ./internal/store -run '^TestCentralS3F2T4$' -count=1
"$binary" data export --db "$source_db" --out "$archive"
"$binary" data import --db "$central_db" --dry-run "$archive"
"$binary" data import --db "$central_db" "$archive"
first="$($binary data import --db "$central_db" "$archive")"
printf '%s\n' "$first" | rg '0 inserted' >/dev/null
"$binary" backup --db "$central_db" --out "$temp_dir/rollback.db"
test -s "$temp_dir/rollback.db"
echo "central cutover and rollback fixture passed"
