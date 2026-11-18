---
title: Docker Compose Install
doc_type: howto
---

# Docker Compose Install

For v5.0.0, no prebuilt image is published. Use the release archives for a binary installation, or build the tagged source for Docker Compose.

## Build your own container image

Choose the release tag and Linux architecture used by your deployment (`amd64` or `arm64`). Build on a host with Docker and BuildKit. The tag below matches the image reference in `deploy/docker-compose.yml`.

```bash
RELEASE_TAG=v5.0.0
arch=amd64
git clone --branch "$RELEASE_TAG" --depth 1 https://github.com/metrum-ai/router.git router
cd router
export METRUM_AI_ROUTER_VERSION="${RELEASE_TAG}-linux-${arch}"
docker build --platform "linux/$arch" \
  --build-arg VERSION="$RELEASE_TAG" --build-arg COMMIT="$(git rev-parse HEAD)" \
  --build-arg BUILD_DATE="$(git show -s --format=%cI HEAD)" \
  -t "metrum-ai-router:$METRUM_AI_ROUTER_VERSION" .
mkdir -p ../compose/{config,state,logs}
cp deploy/docker-compose*.yml deploy/Caddyfile.compose ../compose/
cp config.example.yaml ../compose/config/config.yaml
cp env.example.json ../compose/config/env.json
cd ../compose
printf 'METRUM_AI_ROUTER_VERSION=%s\n' "$METRUM_AI_ROUTER_VERSION" > .env
```

Use a new deployment directory for these commands. Compose mounts its local config, state and log directories. The default profile uses SQLite.

Generate at least one caller token and replace the placeholder caller hashes in `config/config.yaml` before first startup:

```bash
docker run --rm \
  --entrypoint /app/bin/metrum-ai-router-token-gen \
  "metrum-ai-router:${METRUM_AI_ROUTER_VERSION}" \
  generate \
  --owner-user example-admin \
  --project example-project \
  --env prod \
  --allow <allowed-model-group>[,<allowed-model-group>...]
```

Save the printed raw token for the caller through an approved secret channel. In `config/config.yaml`, add or verify the referenced `users`, `projects`, and `project_memberships` entries, then replace the placeholder `callers:` entries with the generated caller YAML. A fresh template with `REPLACE_WITH_TOKEN_HASH_OF_*` values is intentionally not startable.

The packaged production example binds to `127.0.0.1:8080`, enables security reports and stops each provider at a configured hourly ceiling of USD 1. Review the ceilings for your workload. Replace each caller credential block with its generated production ID, verifier, format, entropy and actual UTC issuance date. Run `docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-routerctl router doctor --security --config /app/config/config.yaml --fail-on warn` after provisioning. Use `config/config.dev.example.yaml` only for local development. With Caddy in a separate container, configure Router TLS on the private network listener using a certificate valid for the service name `router`. Change the packaged Caddy upstream to `reverse_proxy https://router:8080` and install the issuing CA in the Caddy container’s trust store.

Edit `config/config.yaml` for durable container paths before startup:

```yaml
server:
  listen: ":8080"
  tls:
    mode: router
    certificate_source: files
    cert_file: /app/config/tls/router.crt
    key_file: /app/config/tls/router.key
  logging:
    path: /app/logs/requests.jsonl
  usage_db:
    enabled: true
    driver: sqlite
    path: /app/state/usage.sqlite
    migration_policy: deployment-job

state_path: /app/state/router-state.json
```

SQLite supports one active router writer. Compose persists its database in `./state`; do not use shared or multi-replica storage. For a multi-replica or externally managed database deployment, explicitly add `docker-compose.postgres-localhost.yml`, configure `server.usage_db.driver: postgres` and `dsn: ${ROUTER_USAGE_DB_DSN}`, and supply the deployment-owned DSN/password.

## Configure Secrets

Place provider credentials in `config/env.json`. Keep this file in deployment secret storage or protected host storage, outside packages, tickets, and shared support bundles, and restrict filesystem permissions.

```json
{
  "OPENAI_API_KEY": "replace-with-provider-key",
  "OPENROUTER_API_KEY": "replace-with-provider-key"
}
```

Then set ownership and permissions for the container runtime user. The image built from the release Dockerfile runs as UID/GID `65532`; the config directory must be readable and traversable, and state/log directories must be writable by that ID.

```bash
sudo chown -R 65532:65532 config state logs
sudo chmod 0400 config/env.json
```

## Run The Migration Gate, Then Start And Validate

For `server.usage_db.migration_policy: deployment-job`, stop or keep the router absent while running the non-serving gate. Before upgrades, take one approved atomic offline snapshot/copy of `usage.sqlite` together with any `-wal`/`-shm` sidecars; never copy those files independently while the router writes. For a fresh SQLite PVC/state bind, run:

```bash
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --version
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=plan --driver=sqlite --db=/app/state/usage.sqlite --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=apply --driver=sqlite --db=/app/state/usage.sqlite --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=resume --job=historical-usage-validation-v1 --checkpoint-ordinal=0 --driver=sqlite --db=/app/state/usage.sqlite --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=verify-serving --driver=sqlite --db=/app/state/usage.sqlite --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=status --driver=sqlite --db=/app/state/usage.sqlite --json
```

`verify-serving` runs schema postconditions and fails unless the ledger is current/compatible and every bound data job is validated. It is the machine gate immediately before final read-only `status`; ordinal `0` alone does not prove completion. PostgreSQL uses the explicit override and `--driver postgres --dsn-env ROUTER_USAGE_DB_DSN` instead.

```bash
docker compose config >/dev/null
docker compose up -d
docker compose ps
```

Then verify from a network location that represents the intended clients:

```bash
export ROUTER_BASE_URL="https://<router-host>"
export ROUTER_TOKEN="replace-with-router-token"

curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/docs/"
curl -fsS "$ROUTER_BASE_URL/version"
curl -fsS -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

Run one small completion through an allowed model group:

```bash
curl -fsS "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "replace-with-allowed-model-group",
    "messages": [{"role": "user", "content": "Reply OK only."}],
    "max_tokens": 16
  }'
```

## Operational Notes

- Terminate TLS at a deployment-managed reverse proxy.
- Keep Postgres private to the deployment network unless a reviewed localhost-only administration override is used.
- Scrape `/metrics` only with a caller authorized for metrics administration.
- Keep `compose/config/`, `compose/state/`, database volumes, logs, and backups under the deployment's secret-handling policy.
- Back up `config.yaml` and the usage database before upgrades.

## Upgrade And Rollback

Before an upgrade, back up `compose/.env`, `compose/config/`, `compose/state/`, Postgres data, logs needed by the retention policy, and the previous local image. Build the new release tag, update `METRUM_AI_ROUTER_VERSION` to the new local image tag, review config template changes, run `docker compose config`, then recreate the router service.

After restart, repeat `/readyz`, `/docs/`, `/v1/models`, one caller smoke, and an admin report smoke when reports are enabled.

Package rollback never runs a reverse migration. Read the release migration contract: if it is `restore-required`, restore the approved pre-migration snapshot before deploying the earlier package. Otherwise preserve the usage database and roll back only the approved package/config inputs, then rerun the migration verify/status gate and the same smokes before sending traffic.

For release-to-release sequencing, see [Release Notes And Upgrades](../release-notes/upgrade-guide).
