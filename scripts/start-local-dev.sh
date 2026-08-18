#!/usr/bin/env bash
# Start the local grok2api dev instance that was synced from gk.ss.zoooo.vip.
# Requires: Docker postgres grok2api-local-postgres and repo-root config.yaml (gitignored).
# Uses the public production proxy pool at 23.238.50.37:38005.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ ! -f "$ROOT/config.yaml" ]]; then
  echo "missing $ROOT/config.yaml" >&2
  exit 1
fi

if ! docker inspect grok2api-local-postgres >/dev/null 2>&1; then
  echo "starting local postgres..."
  docker run -d --name grok2api-local-postgres \
    --network grok2api-local-test \
    -e POSTGRES_DB=grok2api \
    -e POSTGRES_USER=grok2api \
    -e POSTGRES_PASSWORD=local-test-password \
    -p 25432:5432 \
    postgres:18-alpine
fi
if [[ "$(docker inspect -f '{{.State.Running}}' grok2api-local-postgres 2>/dev/null || true)" != "true" ]]; then
  docker start grok2api-local-postgres
fi

if lsof -nP -iTCP:18000 -sTCP:LISTEN >/dev/null 2>&1; then
  echo "already listening on 127.0.0.1:18000"
  exit 0
fi

export GOCACHE="$ROOT/.gocache"
cd "$ROOT/backend"
exec go run ./cmd/grok2api --config "$ROOT/config.yaml"
