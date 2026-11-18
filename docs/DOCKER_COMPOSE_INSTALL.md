# Docker Compose Package Quick Install

Starting with v5.0.0-rc.4, releases publish binary archives and no prebuilt container image. For offline deployment, check out the release tag and run `make package-docker` on your build host. Transfer the resulting Compose package for the target architecture to the deployment host. After startup, use the embedded `/docs/` site as the primary administrator reference.

## Prerequisites

- A Linux host matching the package architecture: `docker-linux-amd64` or `docker-linux-arm64`.
- Docker Engine and the Docker Compose plugin.
- A TLS reverse proxy or ingress in front of the router.
- An operator-generated `license.json` and paired verification public key.
- A single router writer and private `state`/`logs` directories. New installs use SQLite; PostgreSQL is an explicit multi-replica or externally managed database choice.

## Bootstrap

```bash
tar -xzf metrum-ai-router-<version>-docker-linux-<arch>.tar.gz
cd metrum-ai-router-<version>-docker-linux-<arch>

docker load -i images/metrum-ai-router-<version>-linux-<arch>.tar

cd compose
mkdir -p config state logs
cp ../config/config.example.yaml config/config.yaml
cp ../config/env.example.json config/env.json
cp -R ../config/scripts config/scripts
```

The packaged production example binds to `127.0.0.1:8080`, enables security reports and stops each provider at a configured hourly ceiling of USD 1. Review the ceilings for your workload. Replace each caller credential block with its generated production ID, verifier, format, entropy and actual UTC issuance date. Run `metrum-ai-routerctl doctor --security --config config/config.yaml --fail-on warn` after provisioning. Use `config/config.dev.example.yaml` only for local development. With Caddy in a separate container, configure Router TLS on the private network listener using a certificate valid for the service name `router`. Change the packaged Caddy upstream to `reverse_proxy https://router:8080` and install the issuing CA in the Caddy container’s trust store.

Review `compose/.env`; only `METRUM_AI_ROUTER_VERSION` is required and must be the package image tag, never `latest`. Configure exact container paths before the migration gate:
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

Place the issued license at `config/license.json`, matching `server.license.path` in `config/config.yaml`. Put provider credentials in `config/env.json` or the deployment secret manager.

Generate a caller token from your locally built image and add the generated caller entry to `config/config.yaml`:

```bash
docker run --rm \
  --entrypoint /app/bin/metrum-ai-router-token-gen \
  metrum-ai-router:<version>-linux-<arch> \
  generate \
  --owner-user example-admin \
  --project example-project \
  --env prod \
  --allow <allowed-model-group>[,<allowed-model-group>...]
```

Set permissions for the container runtime user used by your locally built image:

```bash
sudo chown -R 65532:65532 config state logs
chmod 0700 config config/scripts state logs
chmod 0400 config/env.json config/license.json
```

Migrations run only while the router is stopped. Before an upgrade, take one approved atomic storage snapshot or offline copy of `usage.sqlite` together with any `-wal`/`-shm` sidecars; never copy those files independently while the router writes. Run the non-serving SQLite gate in order:
```bash
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --version
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=plan --driver=sqlite --db=/app/state/usage.sqlite --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=apply --driver=sqlite --db=/app/state/usage.sqlite --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=resume --job=historical-usage-validation-v1 --checkpoint-ordinal=0 --driver=sqlite --db=/app/state/usage.sqlite --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=verify-serving --driver=sqlite --db=/app/state/usage.sqlite --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=status --driver=sqlite --db=/app/state/usage.sqlite --json
```
`verify-serving` checks schema postconditions and fails unless the ledger is current/compatible and every bound data job is validated. It is the machine gate immediately before final read-only `status`; ordinal `0` alone is not completion evidence.

Start the service only after compatible/current final status:

```bash
docker compose config >/dev/null
docker compose up -d
docker compose ps
```

The base Compose profile has no database credentials or TCP database egress and persists SQLite on `./state:/app/state`. For PostgreSQL, explicitly include `docker-compose.postgres-localhost.yml`, set `POSTGRES_PASSWORD` and `ROUTER_USAGE_DB_DSN`, and configure `server.usage_db.driver: postgres` with `dsn: ${ROUTER_USAGE_DB_DSN}`. The override binds Postgres only to `127.0.0.1`.

## Validate

```bash
export ROUTER_BASE_URL="https://router.example.com"
export ROUTER_TOKEN="replace-with-router-token"

curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/docs/"
curl -fsS -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

If browser admin reports are enabled, verify the authorized report surface with the deployment's browser-admin identity:

```text
https://router.example.com/admin/reports/
```

## Upgrade And Rollback

Before upgrading, back up `compose/.env`, `compose/config/`, `compose/state/`, and logs according to the deployment policy. For SQLite, the usage DB and sidecars require one offline atomic copy; for an explicit PostgreSQL installation, use its approved consistent database backup. Build the new tag and load its image tar, update `METRUM_AI_ROUTER_VERSION`, review config changes, run `docker compose config`, then recreate the router.

Package rollback never runs a reverse migration. For a `restore-required` release contract, restore the approved pre-migration database snapshot before deploying the earlier package; otherwise preserve the usage database and roll back only approved package/config inputs. Rerun migration verify/status, `/readyz`, `/docs/`, `/v1/models`, and one caller smoke.
