---
title: Package Validation And Security Checks
doc_type: howto
---

# Package Validation And Security Checks

Release packages are designed to be small, inspectable, and safe for external administrators. The router-served `/docs/` site is the primary external reference; archive Markdown is only an offline bootstrap set for starting the router.

## What To Verify

For binary packages:

```text
metrum-ai-router-<version>-linux-<arch>/
  bin/metrum-ai-router
  bin/metrum-ai-router-token-gen
  bin/metrum-ai-router-usage-report
  bin/metrum-ai-router-migrate
  bin/metrum-ai-routerctl
  config/config.example.yaml
  config/env.example.json
  config/scripts/router.ts
  caddy/Caddyfile
  docs/
```

Windows binary ZIPs use the same top-level layout and files as Linux packages,
with `.exe` suffixes on the packaged binaries:

```text
metrum-ai-router-<version>-windows-<arch>/
  bin/metrum-ai-router.exe
  bin/metrum-ai-router-token-gen.exe
  bin/metrum-ai-router-usage-report.exe
  bin/metrum-ai-router-migrate.exe
  bin/metrum-ai-routerctl.exe
  config/config.example.yaml
  config/env.example.json
  config/scripts/router.ts
  caddy/Caddyfile
  docs/
  LICENSE
  NOTICE
  THIRD_PARTY_NOTICES.md
  MODEL_LICENSES.md
```

Windows ZIPs are named `metrum-ai-router-<version>-windows-amd64.zip` or
`metrum-ai-router-<version>-windows-arm64.zip`. The default release does not
require Authenticode signing. Verify build provenance and `SHA256SUMS` as
explained in [Binary Install](./binary); unsigned executables may trigger
SmartScreen. Authenticode signing remains an optional, separately provisioned
release path, not a guarantee for every release. Apply your organization's
executable approval controls. The package does not install a Windows service,
and the included Caddyfile is an example rather than a bundled Caddy service.

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

`metrum-ai-routerctl` is required in the binary package and is included with
the other canonical runtime CLIs in the standard Docker image and Docker
Compose package image.

Confirm the architecture suffix matches the host and, for Docker packages, that
`compose/.env` pins the packaged image tag in `METRUM_AI_ROUTER_VERSION`.
Older installs may still have the legacy `SMART_LLMROUTER_VERSION` line;
the package upgrade script rewrites it to `METRUM_AI_ROUTER_VERSION`.

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

An accepted release set contains Linux binary packages and Windows binary ZIPs
for `amd64` and `arm64`, plus one Linux Docker package for each of
`linux/amd64` and `linux/arm64` when Docker packages are part of the delivery.
Its `SHA256SUMS` and `release-artifacts.json` must bind every published filename
to its byte size, SHA-256, platform, release version, source commit, and UTC
build date. Download all published assets into an empty directory and verify
the checksums there; a checksum generated only from the builder's local copy is
not publication evidence. On Windows, use PowerShell and compare the result to
the matching line in the downloaded `SHA256SUMS`:

```powershell
Get-FileHash .\metrum-ai-router-<version>-windows-amd64.zip -Algorithm SHA256
```

Extract a Windows package and run `bin\metrum-ai-router.exe --version` to
confirm the executable starts. The package does not configure service
supervision; run it in a console for evaluation or configure your approved
Windows process supervisor separately.

## What Must Not Be Present

Packages should not contain:

- deployment-private operating notes;
- private hostnames, IP addresses, SSH users, SSH key paths, or live production paths;
- raw provider keys, raw router tokens, token hashes, GitHub tokens, or signing keys;
- leftover `license.json` or other local state (unused in 3.0.0+), usage databases, logs, JSONL state, or router state files;
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
