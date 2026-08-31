#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

CGO_ENABLED=0 go test -p 1 -count=1 ./internal/ingest ./internal/ingestserver ./internal/store ./internal/web -run 'Central|Device|Backup|Ingest'

if rg -n -i --glob '!*.sum' --glob '!docs/central-hosting-operations.md' '(BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|Bearer [A-Za-z0-9._-]{24,}|access_token["= :]+[A-Za-z0-9._-]{20,})' deploy internal/collector internal/ingest internal/ingestserver internal/store/central.go; then
  echo "possible tracked secret in central-hosting scope" >&2
  exit 1
fi

if rg -n "ports:.*9211|[\"']?[0-9.]*:9211:9211" deploy/server2/docker-compose.yml; then
  echo "dashboard port must not be host-published" >&2
  exit 1
fi

rg -n '127\.0\.0\.1:\$\{ONWATCH_INGEST_HOST_PORT' deploy/server2/docker-compose.yml >/dev/null
echo "central hosting security checks passed"
