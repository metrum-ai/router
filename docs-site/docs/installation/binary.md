---
title: Binary Install
doc_type: howto
---

# Binary Install

The binary package is for teams that already operate Linux services with their own process supervisor, database, log pipeline, and TLS proxy.

For package selection and architecture guidance, start with [Deployment Artifacts](./deployment-artifacts). For package inspection and security checks, see [Package Validation And Security Checks](./package-validation).

## Package Layout

```text
metrum-ai-router-<version>-linux-<arch>/
  bin/
    metrum-ai-router
    metrum-ai-router-token-gen
    metrum-ai-router-usage-report
    metrum-ai-router-migrate
    metrum-ai-routerctl
  config/
    config.example.yaml
    env.example.json
    scripts/
      router.ts
  caddy/
    Caddyfile
  docs/
```

Use `linux-amd64` for x86_64 hosts and `linux-arm64` for ARM64 hosts. Release validation checks the package binaries against the selected architecture and rejects unexpected files, deployment-private notes, raw secrets, local state, and macOS archive metadata.

`metrum-ai-routerctl` provides customer-local configuration, caller-token,
model, and usage operations. The default deployment is SQLite state with one
Router process; it neither provisions nor binds RDS.

Create a dedicated service account, then create deployment-owned directories:

```bash
sudo groupadd --system router
sudo useradd --system --gid router --home-dir /var/lib/metrum-ai-router --shell /usr/sbin/nologin router
sudo install -d -m 0750 -o router -g router /etc/metrum-ai-router
sudo install -d -m 0750 -o router -g router /var/lib/metrum-ai-router
sudo install -d -m 0750 -o router -g router /var/log/metrum-ai-router
```

If the deployment uses a different service account, substitute that account consistently in the install commands and process supervisor configuration.

Create reviewed runtime files from the shipped templates before installing them:

```bash
cp config/config.example.yaml config/config.yaml
cp config/env.example.json config/env.json

bin/metrum-ai-router-token-gen generate \
  --owner-user example-admin \
  --project example-project \
  --env prod \
  --allow <allowed-model-group>[,<allowed-model-group>...]
```

Save the printed raw token for the caller through an approved secret channel. In `config/config.yaml`, add or verify the referenced `users`, `projects`, and `project_memberships` entries, then replace the placeholder caller token hashes with the generated `callers:` entry. Populate `config/env.json` or the service environment with provider credentials before startup.

Edit runtime paths in `config/config.yaml` for the binary host layout before installing the file:

```yaml
server:
  logging:
    path: /var/log/metrum-ai-router/requests.jsonl
  usage_db:
    enabled: true
    driver: sqlite
    path: /var/lib/metrum-ai-router/usage.sqlite

state_path: /var/lib/metrum-ai-router/router-state.json
```

Use `driver: postgres` and a deployment-owned DSN instead of SQLite when the binary service is part of a production database deployment.

Install the binary and runtime files according to the host change-control process:

```bash
sudo install -m 0755 bin/metrum-ai-router /usr/local/bin/metrum-ai-router
sudo install -m 0755 bin/metrum-ai-router-token-gen /usr/local/bin/metrum-ai-router-token-gen
sudo install -m 0755 bin/metrum-ai-router-usage-report /usr/local/bin/metrum-ai-router-usage-report
sudo install -m 0755 bin/metrum-ai-routerctl /usr/local/bin/metrum-ai-routerctl
sudo install -m 0640 -o router -g router config/config.yaml /etc/metrum-ai-router/config.yaml
sudo install -m 0640 -o router -g router config/env.json /etc/metrum-ai-router/env.json
```

`metrum-ai-routerctl` is the customer-local operations CLI. On file-owned
installs it validates or diffs local configuration, can write local `config.yaml`
for callers/providers/model groups (with a timestamped sibling backup), renders a
Kubernetes architecture blueprint from a stack intent, backs up or restores a
SQLite usage database with `--confirm-offline`, generates a caller token into a
new mode-`0600` file, and reports safe local configuration, model, and
aggregate-usage status. It cannot activate configuration on a remote managed
hostname or access cloud or Kubernetes APIs.

Packages ship canonical `metrum-ai-router*` binaries only. Older CLI names remain
as source-only exit-2 notices under `cmd/` and are not packaged.

## Runtime Configuration

The service process needs access to:

- `config.yaml`;
- the provider credential env file;
- the usage database DSN;
- optional routing script files and helper dependencies already packaged on disk.

Example service command:

```bash
metrum-ai-router \
  --config /etc/metrum-ai-router/config.yaml
```

The router loads `env.json` from the same directory as the config file before expanding `${VAR}` references. Deployments that use a secret manager can inject the same environment variables into the service process instead.

Do not configure the router to download code or packages at runtime. TypeScript policy dependencies and helper files must be packaged before deployment.

## Run The Migration Gate Before Service Start

For `server.usage_db.migration_policy: deployment-job`, use the packaged non-serving runner and its canonical `docs/DATA_MIGRATIONS.md` runbook before a fresh service start: `plan`, approved backup, `apply`, every required data job until its safe state is `validated`, `verify`, `status`, then serve. `auto-safe` is not a PostgreSQL production procedure. For PostgreSQL, provide the connection through the protected service environment and name it with `--dsn-env`; never put a DSN in a command line, ticket, or log. A successful checkpoint ordinal `0` does not establish service readiness. For SQLite, use exclusive downtime, an SQLite-safe backup, integrity verification, and free-space checks before the gate. The metrics-admin migration summary and authenticated read-only **Operations / Data migrations** report verify safe state after startup; they do not apply or reverse migrations.

## Validate

```bash
export ROUTER_BASE_URL="https://<router-host>"
export ROUTER_TOKEN="replace-with-router-token"

curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/docs/"
curl -fsS "$ROUTER_BASE_URL/version"
curl -fsS -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

Expected results:

- `/readyz` returns success only when required runtime checks pass.
- `/docs/` serves the embedded product documentation from the running binary.
- `/version` returns safe release metadata.
- `/v1/models` returns only model groups allowed for the caller token.

For request-level diagnostics after installation, use [Troubleshooting Requests](../troubleshooting/requests).

## Upgrade And Rollback

Before an upgrade, back up `config.yaml`, `env.json` or equivalent secret-manager state, router state, usage database data, logs needed by the retention policy, and the previous package artifact.

Install the new package beside the old package, run `metrum-ai-router --version` or `bin/metrum-ai-router --version`, review config template changes, then restart the supervised service with the new binary. After restart, repeat `/readyz`, `/docs/`, `/v1/models`, and one caller smoke.

Package rollback never runs a reverse migration. If the release migration contract is `restore-required`, restore the approved pre-migration snapshot before deploying the earlier binary. Otherwise preserve the usage database and restore only approved package/config inputs, then repeat migration verify/status and the same smokes before sending traffic.
