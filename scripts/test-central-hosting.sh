#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

sh scripts/check-gofmt.sh
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -p 1 -count=1 ./...
CGO_ENABLED=0 go build -o /tmp/onwatch-central-hosting-test .
node --check internal/web/static/app.js
git diff --check
docker compose -f deploy/server2/docker-compose.yml config >/dev/null
bash scripts/test-central-hosting-security.sh
bash scripts/validate-central-cutover-manifest.sh docs/central-cutover-manifest.example

if [[ "${ONWATCH_TEST_DOCKER:-0}" == 1 ]]; then
  bash scripts/test-central-hosting-deploy.sh
fi

echo "central hosting regression gate passed"
