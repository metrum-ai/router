---
title: Package Validation And Security Checks
doc_type: howto
---

# Package Validation And Security Checks

Release packages are designed to be small, inspectable, and safe for external administrators. The router-served `/docs/` site is the primary external reference; tarball Markdown is only an offline bootstrap set for starting the service.

## What To Verify

For binary packages:

```text
metrum-ai-router-<version>-linux-<arch>/
  bin/metrum-ai-router
  bin/metrum-ai-router-token-gen
  bin/metrum-ai-router-usage-report
  bin/metrum-ai-router-migrate
  bin/metrum-ai-routerctl
  bin/metrum-ai-router-fleetctl
  bin/metrum-ai-router-fleet-sign
  config/config.example.yaml
  config/env.example.json
  config/scripts/router.ts
  caddy/Caddyfile
  docs/
```

For Docker Compose packages:

```text
metrum-ai-router-<version>-docker-linux-<arch>/
  compose/docker-compose.yml
  compose/docker-compose.postgres-localhost.yml
  compose/Caddyfile.compose
  compose/.env.example
  compose/.env
  config/config.example.yaml
  config/env.example.json
  config/scripts/router.ts
  images/metrum-ai-router-<version>-linux-<arch>.tar
  docs/
```

`metrum-ai-routerctl` and `metrum-ai-router-fleetctl` are
required in the binary package. Only customer-local
`metrum-ai-routerctl` (with the other canonical runtime CLIs) is included in
the standard Docker image and Docker Compose package image.
Fleet lifecycle binaries are excluded; run Fleet work from an extracted
binary package on a separate trusted administration host.

Confirm the architecture suffix matches the host and, for Docker packages, that
`compose/.env` pins the packaged image tag. Until the PR3 compose env rename
lands, existing packages may still use `SMART_LLMROUTER_VERSION`; new docs and
the product contract use `METRUM_AI_ROUTER_VERSION`.

For Docker packages, the saved image includes `/app/bin/metrum-ai-router-migrate` and
`/app/bin/metrum-ai-routerctl`. Version-check them before operation:

```bash
docker run --rm --entrypoint /app/bin/metrum-ai-routerctl \
  metrum-ai-router:<version>-linux-<arch> version
```

For `migration_policy: deployment-job`, follow the packaged `docs/DATA_MIGRATIONS.md` runbook before service startup: `plan`, approved backup, `apply`, every release-defined data job until each safe state is `validated`, `verify-serving`, then final read-only `status`. `verify-serving` runs schema postconditions and fails unless the ledger is current/compatible and every bound data job is validated. Generic Compose/Kubernetes installs use `--driver sqlite --db /app/state/usage.sqlite`; PostgreSQL is a separately configured deployment substitution using `--driver postgres --dsn "$ROUTER_USAGE_DB_DSN"`. Do not treat checkpoint ordinal `0` as completion or expose a connection string in commands/evidence. See [Upgrade Guide](../release-notes/upgrade-guide) for the release and rollback contract.

## Release Validation Matrix

Run the release validation matrix before accepting or distributing artifacts. The matrix should cover package metadata validation, package content validation, Docker image checks, Compose security checks, and Kubernetes overlay rendering when Kubernetes artifacts are part of the delivery.

During artifact intake, include inspection against the package allowlist and denylist. Artifact inspection does not replace runtime smoke tests. For Docker Compose or Kubernetes deployments, still start the packaged router in the target environment and verify `/readyz`, `/docs/`, `/version`, `/v1/models`, one authenticated model request, admin reports when enabled, and metrics/admin denial for ordinary caller tokens.

An accepted release set contains one binary package and one Docker package for
each of `linux/amd64` and `linux/arm64`. Its `SHA256SUMS` and
`release-artifacts.json` must bind every filename to its byte size, SHA-256,
platform, release version, source commit, and UTC build date. Download all
published assets into an empty directory and verify the checksums there; a
checksum generated only from the builder's local copy is not publication
evidence.

## What Must Not Be Present

Packages should not contain:

- deployment-private operating notes;
- private hostnames, IP addresses, SSH users, SSH key paths, or live production paths;
- raw provider keys, raw router tokens, token hashes, GitHub tokens, or signing keys;
- real `license.json`, license state files, local usage databases, logs, JSONL state, or router state files;
- compiler/toolchain directories, local working directories, or local temporary output;
- full production configs or ignored local config snapshots.

If any of those are present, stop the deployment and request a corrected package.

## Runtime Checks

After startup:

```bash
export ROUTER_BASE_URL="https://<router-host>"
export ROUTER_TOKEN="replace-with-router-token"

curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/version"
curl -fsS "$ROUTER_BASE_URL/docs/"
curl -fsS -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

Then run one small completion against a model group returned by `/v1/models`.

When admin reports are enabled, verify `/admin/reports/` only with a browser-admin subject authorized by the deployment. Ordinary caller tokens should not receive report access. Metrics scraping should use a caller subject authorized for metrics administration, not normal application keys.

## Evidence To Keep

Record the package name, router version, build timestamp, architecture, config checksum, image tag when applicable, `/readyz` result, `/docs/` result, `/v1/models` result for each caller class, and one request ID from a successful smoke.

Keep evidence free of raw tokens, provider keys, token hashes, prompt content, raw images, full config files, and private support-only paths.
