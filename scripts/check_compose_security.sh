#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

BASE_ENV="$TMP_DIR/base.env"
cat >"$BASE_ENV" <<'ENV'
METRUM_AI_ROUTER_VERSION=test-secure-linux-amd64
ROUTER_HOSTNAME=router.example.com
CADDY_EMAIL=ops@example.com
CADDY_HTTP_PORT=8080
CADDY_HTTPS_PORT=8443
ENV

OPT_IN_ENV="$TMP_DIR/postgres.env"
cat >>"$OPT_IN_ENV" <<'ENV'
METRUM_AI_ROUTER_VERSION=test-secure-linux-amd64
POSTGRES_DB=llmrouter
POSTGRES_USER=llmrouter
POSTGRES_PASSWORD=test-secure-compose-password-42
ROUTER_USAGE_DB_DSN=host=postgres port=5432 user=llmrouter password=test-secure-compose-password-42 dbname=llmrouter sslmode=disable TimeZone=UTC
POSTGRES_HOST_PORT=15432
ENV

if env -u METRUM_AI_ROUTER_VERSION docker compose --env-file /dev/null -f "$ROOT_DIR/deploy/docker-compose.yml" config >"$TMP_DIR/missing-image.out" 2>&1; then
  echo "expected base docker compose config to fail without a pinned image version" >&2
  exit 1
fi

BASE_JSON="$TMP_DIR/base.json"
docker compose --env-file "$BASE_ENV" -f "$ROOT_DIR/deploy/docker-compose.yml" config --format json >"$BASE_JSON"

python3 - "$BASE_JSON" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    cfg = json.load(f)

services = cfg["services"]
if "postgres" in services:
    raise SystemExit("base Compose profile must not include postgres")
router = services["router"]
if router.get("working_dir") != "/app/state":
    raise SystemExit(f"router working_dir must be /app/state: {router.get('working_dir')!r}")
if not any(volume.get("source", "").endswith("/state") and volume.get("target") == "/app/state" for volume in router.get("volumes", [])):
    raise SystemExit(f"router must bind durable state: {router.get('volumes')!r}")
if "ROUTER_USAGE_DB_DSN" in (router.get("environment") or {}):
    raise SystemExit("base router must not receive ROUTER_USAGE_DB_DSN")
if "postgres" in (router.get("depends_on") or {}):
    raise SystemExit("base router must not depend on postgres")
if "postgres_data" in (cfg.get("volumes") or {}):
    raise SystemExit("base Compose profile must not include postgres_data")
if router["image"].endswith(":latest"):
    raise SystemExit(f"router image must not resolve to latest: {router['image']}")
PY

for missing in POSTGRES_PASSWORD ROUTER_USAGE_DB_DSN; do
  MISSING_ENV="$TMP_DIR/missing-$missing.env"
  python3 - "$OPT_IN_ENV" "$MISSING_ENV" "$missing" <<'PY'
from pathlib import Path
import sys

source, destination = map(Path, sys.argv[1:3])
name = sys.argv[3]
destination.write_text("".join(line for line in source.read_text().splitlines(keepends=True) if not line.startswith(f"{name}=")))
PY
  if docker compose --env-file "$MISSING_ENV" -f "$ROOT_DIR/deploy/docker-compose.yml" -f "$ROOT_DIR/deploy/docker-compose.postgres-localhost.yml" config >"$TMP_DIR/missing-$missing.out" 2>&1; then
    echo "expected PostgreSQL override to fail without $missing" >&2
    exit 1
  fi
done

OPT_IN_JSON="$TMP_DIR/postgres.json"
docker compose --env-file "$OPT_IN_ENV" -f "$ROOT_DIR/deploy/docker-compose.yml" -f "$ROOT_DIR/deploy/docker-compose.postgres-localhost.yml" config --format json >"$OPT_IN_JSON"

python3 - "$OPT_IN_JSON" <<'PY'
import json
import sys

expected_dsn = "host=postgres port=5432 user=llmrouter password=test-secure-compose-password-42 dbname=llmrouter sslmode=disable TimeZone=UTC"
with open(sys.argv[1], encoding="utf-8") as f:
    cfg = json.load(f)

router = cfg["services"]["router"]
if router["environment"].get("ROUTER_USAGE_DB_DSN") != expected_dsn:
    raise SystemExit("PostgreSQL override must pass its exact DSN to router")
if router.get("depends_on", {}).get("postgres", {}).get("condition") != "service_healthy":
    raise SystemExit("PostgreSQL override must wait for postgres health")
postgres = cfg["services"]["postgres"]
if postgres.get("environment", {}).get("POSTGRES_PASSWORD") in {"", "llmrouter", "change-this-password", "replace-with-strong-random-db-password"}:
    raise SystemExit("postgres password resolved to an unsafe example value")
if not postgres.get("healthcheck"):
    raise SystemExit("PostgreSQL override must define a healthcheck")
if not any(volume.get("source") == "postgres_data" and volume.get("target") == "/var/lib/postgresql" for volume in postgres.get("volumes", [])):
    raise SystemExit("PostgreSQL override must persist its database")
ports = postgres.get("ports") or []
if len(ports) != 1:
    raise SystemExit(f"expected one localhost postgres port, got {ports!r}")
port = ports[0]
if port.get("host_ip") != "127.0.0.1" or str(port.get("published")) != "15432" or str(port.get("target")) != "5432":
    raise SystemExit(f"postgres override must bind 127.0.0.1:15432->5432, got {port!r}")
PY

echo "compose security checks: ok"
