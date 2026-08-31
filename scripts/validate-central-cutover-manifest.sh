#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-}"
if [[ -z "$manifest" || ! -f "$manifest" ]]; then
  echo "usage: $0 MANIFEST" >&2
  exit 2
fi

required=(release_sha ansible_sha prior_release_sha server2_tailscale_host dashboard_host old_runtime history_export raw_backup provider_owner rollback_command evidence_directory)
for key in "${required[@]}"; do
  value="$(sed -n "s/^${key}=//p" "$manifest" | head -n 1)"
  if [[ -z "$value" ]]; then
    echo "missing manifest field: $key" >&2
    exit 1
  fi
done

if rg -n -i '(bearer[[:space:]]+[A-Za-z0-9._-]{16,}|access[_-]?token=|refresh[_-]?token=|password=[^R])' "$manifest"; then
  echo "manifest appears to contain a raw credential" >&2
  exit 1
fi

echo "central cutover manifest is structurally complete"
