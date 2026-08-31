#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
compose="$root/deploy/server2/docker-compose.yml"
project="onwatch-central-test"
temp_dir="$(mktemp -d)"
network_created=0

cleanup() {
  ONWATCH_DATA_DIR="$temp_dir/data" ONWATCH_BACKUP_DIR="$temp_dir/backups" ONWATCH_ENV_FILE="$root/deploy/server2/onwatch.env.example" ONWATCH_INGEST_HOST_PORT=19212 docker compose -p "$project" -f "$compose" down --remove-orphans >/dev/null 2>&1 || true
  if [[ "$network_created" == 1 ]]; then docker network rm homelab >/dev/null 2>&1 || true; fi
  rm -rf "$temp_dir"
}
trap cleanup EXIT

if ! docker network inspect homelab >/dev/null 2>&1; then
  docker network create homelab >/dev/null
  network_created=1
fi
mkdir -p "$temp_dir/data" "$temp_dir/backups"
chmod 0777 "$temp_dir/data" "$temp_dir/backups"

export ONWATCH_DATA_DIR="$temp_dir/data"
export ONWATCH_BACKUP_DIR="$temp_dir/backups"
export ONWATCH_ENV_FILE="$root/deploy/server2/onwatch.env.example"
export ONWATCH_INGEST_HOST_PORT=19212
export ONWATCH_IMAGE_TAG=test
export ONWATCH_RELEASE_SHA=test-fixture-sha

docker compose -p "$project" -f "$compose" config >/dev/null
docker compose -p "$project" -f "$compose" up -d --build

for _ in $(seq 1 45); do
  status="$(docker inspect --format '{{.State.Health.Status}}' onwatch 2>/dev/null || true)"
  [[ "$status" == healthy ]] && break
  sleep 2
done
[[ "$(docker inspect --format '{{.State.Health.Status}}' onwatch)" == healthy ]]
[[ "$(docker inspect --format '{{index .Config.Labels "com.mshi.onwatch.revision"}}' onwatch)" == test-fixture-sha ]]
[[ "$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' http://127.0.0.1:19212/v1/heartbeat -d '{}')" == 400 ]]
docker exec onwatch /app/onwatch backup --out /backups/deploy-smoke.db
test -s "$temp_dir/backups/deploy-smoke.db"
docker compose -p "$project" -f "$compose" restart onwatch
for _ in $(seq 1 30); do [[ "$(docker inspect --format '{{.State.Health.Status}}' onwatch 2>/dev/null || true)" == healthy ]] && break; sleep 2; done
docker exec onwatch /app/onwatch healthcheck --url http://127.0.0.1:9211/healthz >/dev/null
echo "central hosting Compose deployment passed"
