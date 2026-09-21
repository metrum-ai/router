---
title: Upgrade Guide
doc_type: reference
---

# Upgrade Guide

Use this guide for customer-managed package upgrades. The exact maintenance window, approval process, and rollback policy belong to the deployment operator.

## Pre-Upgrade Checklist

1. Read the release notes for operator impact, database changes, license changes, and caller-visible behavior.
2. Confirm the page banner and `/version` identify the expected router version and build timestamp.
3. Confirm the package architecture matches the host.
4. Back up runtime config, provider env file, license file, license state, and usage database; verify the backup can be restored within the planned window and has sufficient free space.
5. Record the current router version and build timestamp from `/version`.
6. Confirm `/readyz`, `/v1/models`, metrics, and admin reports are healthy before the change.
7. Prepare a rollback package and the previous reviewed config.
8. For v2.0.0+, inventory every install path, systemd unit, Compose service,
   Kubernetes image, and client `model_provider` that still names `metrum-router*`
   or `metrum-genai-smartrouter-*` CLIs.

## Fleet removal and license config (v4.0.0)

v4.0.0 removes the Fleet lifecycle subsystem. These names are gone, not renamed:

- `metrum-ai-router-fleetctl` (including plan, deploy, status, and licenses)
- `metrum-ai-router-fleet-sign`
- packaged rename stubs for those CLIs

Self-managed installs use the binary, Docker Compose, or Kubernetes manifests
with `metrum-ai-routerctl`. Delete systemd units, Compose services, Kubernetes
Jobs, and CI steps that invoke Fleet CLIs before cutover.

`server.license` is rejected at startup. Remove the block. Do not mount
`license.json`. v3.0.0 still accepts the key with one warning; rollback to
v3.0.0 does not require a license file. This release has no usage-database
migration.

## Licensing removal (v3.0.0)

v3.0.0 does not enforce a license and does not read `license.json`.
`server.license` is ignored with one startup warning in this release and will
be rejected in 4.0.0.

These packaged names from the v2.0.0 table below are **removed**, not renamed:

- `metrum-ai-router-license`
- `metrum-ai-router-customer-lifecycle`

Also removed: `metrum-ai-routerctl license` and `metrum-ai-router-fleetctl licenses`.
Fleet plan/deploy/status and `metrum-ai-router-fleet-sign` stayed through the
v3.0.0 licensing removal, and are removed from current packages.

Before cutover, delete systemd units, Compose services, Kubernetes Jobs, and
CI steps that invoke those binaries. License file and license-state backups
matter only if you may roll back to v2.2.0. This release has no
usage-database migration. Drop dashboards and alerts for the removed license
Prometheus series. License volume mounts are unused.

## Breaking rename (v2.0.0)

v2.0.0 packages ship **canonical binaries only**. There are no packaged rename
stub binaries. Update operators and automation before cutover:

| Old packaged name | New packaged name |
| --- | --- |
| `metrum-router` | `metrum-ai-router` |
| `metrum-router-token-gen` | `metrum-ai-router-token-gen` |
| `metrum-router-usage-report` | `metrum-ai-router-usage-report` |
| `metrum-router-migrate` | `metrum-ai-router-migrate` |
| `metrum-routerctl` | `metrum-ai-routerctl` |
| `metrum-genai-smartrouter-fleetctl` | `metrum-ai-router-fleetctl` |
| `metrum-genai-smartrouter-fleet-sign` | `metrum-ai-router-fleet-sign` |
| `metrum-genai-smartrouter-license` | `metrum-ai-router-license` |
| `metrum-genai-customer-lifecycle` | `metrum-ai-router-customer-lifecycle` |

Also removed from packages and the runtime image: `router`, `router-token-gen`,
`router-usage-report`, `router-migrate`, `smartrouterctl`,
`metrum-genai-smartrouterctl`, `metrum-fleetctl`, `metrum-smartrouterctl`,
`metrum-fleet-sign`, and `router-license`.

Release archives and image tags move from `metrum-router-*` to
`metrum-ai-router-*`. Client examples that set `model_provider="metrum-router"`
must use `metrum-ai-router`.

## Required Migration Gate

For a package whose release contract includes a migration, stop or drain the serving router and follow the packaged `docs/DATA_MIGRATIONS.md` runbook: `metrum-ai-router-migrate plan → approved backup → apply → all required data jobs validated → verify → status → serve`. `deployment-job` never applies application schema at startup; it validates the ledger and fails closed until the contract is current, compatible, and every required bound data job is validated—including the post-apply, pre-resume pending state. PostgreSQL production uses this non-serving job, not `auto-safe`; SQLite requires exclusive downtime, SQLite-safe backup and integrity verification, and free-space checks. Checkpoint ordinal `0` is not completion evidence.

Use `--dsn-env=ROUTER_USAGE_DB_DSN` for PostgreSQL and the documented non-serving container `--entrypoint` for Compose. Do not put a database connection string in commands, tickets, screenshots, or logs.

Releases containing usage migration `2026090901` add optional diagnostics
through the non-null text column
`request_usage.target_region` with an empty default for historical and
unlabelled rows. Plan and verify this migration through the same non-serving
gate; do not hand-add or backfill location claims. Package rollback follows
the migration contract and may require restoring the approved pre-migration
database snapshot.

## Docker Compose Upgrade

```bash
docker load -i images/metrum-ai-router-<version>-linux-<arch>.tar

cd compose
cp .env .env.backup
# Pin the image tag env var to the exact loaded tag (metrum-ai-router:<tag>).
# Until the compose env rename lands, packages may still use SMART_LLMROUTER_VERSION;
# the product contract name is METRUM_AI_ROUTER_VERSION.
docker compose config >/dev/null
docker compose up -d
docker compose ps
```

Validate:

```bash
export ROUTER_BASE_URL="https://<router-host>"
curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/version"
curl -fsS "$ROUTER_BASE_URL/docs/" | head
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-routerctl router version
```

## Binary Host Upgrade

1. Extract `metrum-ai-router-<version>-linux-<arch>.tar.gz`.
2. Install the new `bin/metrum-ai-router*` binaries over the previous names
   (or update PATH/service unit ExecStart paths).
3. Diff `config.example.yaml` against the reviewed runtime config; apply only
   approved changes.
4. Run the migration gate when the release contract requires it.
5. Restart the supervised process and validate `/readyz`, `/version`, and one
   authenticated `/v1/models` call.

## Kubernetes Upgrade

Update the Deployment/Job image to `…/metrum-ai-router:<version>-linux-<arch>`
(or the digest from the release inventory). Migration Jobs must use
`/app/bin/metrum-ai-router-migrate`. Confirm the service account, mounts, and
license/config secrets still match the signed intent.

## Rollback

Prefer rolling back to the previous known-good GitHub Release package. For the
v2.0.0 cutover, that is **v1.4.4** (`metrum-router-*` artifact and image names).
Restore the prior service unit / Compose / Kubernetes references and any
pre-upgrade config or usage DB backup required by the migration contract.
Re-check `/readyz` and `/version` before closing the change.
