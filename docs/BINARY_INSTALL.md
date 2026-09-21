# Binary Package Quick Install

This is the offline bootstrap path for a Linux binary package. After startup, use the embedded `/docs/` site as the primary administrator reference.

## Prerequisites

- A Linux host matching the package architecture: `linux-amd64` or `linux-arm64`.
- A service supervisor such as systemd, runit, or an equivalent platform supervisor.
- A TLS reverse proxy such as Caddy, nginx, or a managed load balancer.
- Deployment-owned config, secret, state, log, and usage database locations.
- An operator-generated `license.json` and paired verification public key.

## Bootstrap

```bash
tar -xzf metrum-ai-router-<version>-linux-<arch>.tar.gz
cd metrum-ai-router-<version>-linux-<arch>

bin/metrum-ai-router --version
cp config/config.example.yaml config/config.yaml
cp config/env.example.json config/env.json
```

Edit `config/config.yaml` for the deployment:

- set `server.listen`;
- set `server.license.path` and `server.license.state_path`;
- set `state_path`;
- configure `server.usage_db`;
- configure providers, model groups, and caller access;
- point TypeScript routing to packaged script paths only when used.

Populate `config/env.json` or the process environment with provider credentials. Keep credentials server-side and restrict file permissions.

Generate a caller token and add the generated caller entry to `config/config.yaml`:

```bash
bin/metrum-ai-router-token-gen generate \
  --owner-user example-admin \
  --project example-project \
  --env prod \
  --allow <allowed-model-group>[,<allowed-model-group>...]
```

Before starting a `deployment-job` router, version-check the non-serving runner and follow the canonical [Data migration framework](DATA_MIGRATIONS.md): `plan`, approved backup, `apply`, every required data job until its safe state is `validated`, `verify`, `status`, then serve. PostgreSQL receives its connection only through `--dsn-env`; `auto-safe` is not a PostgreSQL production procedure. Do not infer completion from checkpoint ordinal `0`.
`metrum-ai-routerctl` provides local configuration, caller-token,
model, and usage operations. The default deployment is SQLite state with one
Router process; it neither provisions nor binds RDS. The hosted
**Operations > Deployment Patterns** page defines the customer-facing input and
approval contract.


Start the router in the foreground only after compatible/current final status:

```bash
bin/metrum-ai-router --config config/config.yaml
```

## Validate

From a client-like network location:

```bash
export ROUTER_BASE_URL="https://router.example.com"
export ROUTER_TOKEN="replace-with-router-token"

curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/docs/"
curl -fsS -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

Then run one small request against a model group returned by `/v1/models`:

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

## Upgrade And Rollback

Before upgrading, back up `config.yaml`, `env.json`, `license.json`, license state, router state, usage database data, and logs according to the deployment policy. Install the new package beside the old one, run `bin/metrum-ai-router --version`, review config changes, then restart the supervised service.

Package rollback never runs a reverse migration. For a `restore-required` release contract, restore the approved pre-migration database snapshot before deploying the earlier package; otherwise preserve the usage database and restore only approved package/config inputs. Rerun migration verify/status, `/readyz`, `/docs/`, `/v1/models`, and one caller smoke.
