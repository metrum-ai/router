# Docker Compose Deployment

Source-only operator runbook. Do not add this file to `scripts/package_docs_allowlist.txt`; package-safe external bootstrap deployment guidance belongs in `docs/DOCKER_COMPOSE_INSTALL.md` and the router-served Docusaurus installation docs.

Docker packages are intended for AWS EC2 or similar hosts where the source tree is not present and no image registry is required. The router image embeds the customer-facing Docusaurus documentation and serves it under `/docs/`; browser requests to `/` redirect there.

Kubernetes deployments use the same packaged image tarballs after loading and pushing the image to a deployment-owned registry, but they should use the manifests under `deploy/kubernetes/` instead of Compose files. Do not copy Compose `.env` files or local volume paths into Kubernetes manifests.

The checked-in `Dockerfile` pins the Go builder image to a patched Go patch release. Keep that tag pinned when updating the toolchain so release images do not silently move onto an unreviewed compiler or standard-library patch level.

The Docker build installs the docs and admin dependencies with `npm ci` in layers keyed by their checked-in package manifests and lockfiles, before copying the rest of the source. Keep those dependency layers ahead of target-architecture arguments and source copies: amd64 and arm64 release packages can then share the verified dependency layers while still rebuilding embedded docs with the requested version, commit, and build date. Do not replace `npm ci` with an unlocked install mode.

## Build Package

From the development machine:

```bash
make package-docker
```

This creates:

```text
dist/metrum-ai-router-<version>-docker-linux-amd64.tar.gz
dist/metrum-ai-router-<version>-docker-linux-arm64.tar.gz
```

Docker image builds use `docker buildx build --load` for each packaged platform.

Package targets require a clean git tree and reject versions containing `-dirty`. Use `ALLOW_DIRTY_PACKAGE=1` only for a local development artifact that will not be shipped. Package and image targets validate build metadata before running build commands. Keep `VERSION`, `COMMIT`, `BUILD_DATE`, `GOOS`, `GOARCH`, `PKG_NAME`, `DIST_DIR`, `IMAGE_NAME`, and `IMAGE_TAG` to the safe release formats accepted by `scripts/validate_build_metadata.py`; shell metacharacters and path traversal are rejected. `make secret-check` also validates `.dockerignore` against local secret/state fixtures so ignored runtime files such as `env.json`, production config snapshots, router token files, license files/state, local DBs, logs, and generated artifacts do not enter the Docker build context.

Each package contains:

```text
images/metrum-ai-router-<version>-linux-<arch>.tar
compose/docker-compose.yml
compose/docker-compose.postgres-localhost.yml
compose/Caddyfile.compose
compose/.env
compose/.env.example
config/config.example.yaml
config/env.example.json
config/scripts/router.ts
docs/PACKAGE_README.md
docs/BINARY_INSTALL.md
docs/DOCKER_COMPOSE_INSTALL.md
docs/KUBERNETES_INSTALL.md
docs/PACKAGE_VALIDATION.md
docs/solution-brief.md
docs/LICENSE.md
LICENSE
NOTICE
THIRD_PARTY_NOTICES.md
MODEL_LICENSES.md
```

Keep the four root legal files together during publication and redistribution.
`LICENSE` covers first-party content under Apache-2.0; `NOTICE` carries
distributed notices; `THIRD_PARTY_NOTICES.md` records dependency and asset
terms and surfaces; and `MODEL_LICENSES.md` records model and dataset term
boundaries. Review unresolved entries for the exact artifact rather than
assuming the first-party license grants third-party rights. A deployment's
`license.json` remains protected runtime policy and must not be added to the
package legal set.

Package docs are copied only from the Tier 2 bootstrap allowlist in `scripts/package_docs_allowlist.txt`. Package tar commands run with `COPYFILE_DISABLE=1` so macOS does not inject AppleDouble `._*` metadata. The package build validates the resulting tarball and fails if it contains AppleDouble entries, unexpected package files, missing allowlisted docs, private production runbooks, private host/IP markers, SSH key paths, live production compose config/env/token paths, local secret/state/license filenames, local DB/log artifacts, or raw token/provider-key patterns. The validator also checks that the package has exactly one image tar matching the package architecture and that the saved image layers include `/app/bin/metrum-ai-router`, `/app/bin/metrum-ai-router-token-gen`, and `/app/bin/metrum-ai-router-usage-report`.

Run the full local package validation before release handoff:

After generating the complete four-package inventory, generate CycloneDX SBOMs
and Grype JSON reports bound to every exact archive. Install reviewed Syft and
Grype versions outside the repository, then run `VERSION=<candidate-version>
DIST_DIR=dist make release-security-evidence`. The command fails closed on an
incomplete matrix, malformed evidence, scanner errors, or Critical findings by
default; set `RELEASE_SECURITY_ARGS="--fail-on high"` when the approved release
policy requires the stricter threshold. Retain the summary, all eight evidence
files, scanner versions, database status, artifact hashes, and finding
dispositions with the release record.

```bash
make package-docker-all
python3 scripts/validate_package_contents.py --allowlist scripts/package_docs_allowlist.txt dist/metrum-ai-router-*-docker-linux-amd64.tar.gz dist/metrum-ai-router-*-docker-linux-arm64.tar.gz
```

When Docker is available, load each packaged image and run version checks before deployment:

```bash
docker load -i images/metrum-ai-router-<version>-linux-<arch>.tar
docker run --rm --entrypoint /app/bin/metrum-ai-router metrum-ai-router:<version>-linux-<arch> --version
docker run --rm --entrypoint /app/bin/metrum-ai-router-token-gen metrum-ai-router:<version>-linux-<arch> --version
docker run --rm --entrypoint /app/bin/metrum-ai-router-usage-report metrum-ai-router:<version>-linux-<arch> --version
docker run --rm --entrypoint /app/bin/metrum-ai-router-migrate metrum-ai-router:<version>-linux-<arch> --version
```

## AWS EC2 Host Setup

Use the amd64 package for common x86_64 EC2 instances and arm64 for Graviton instances.

Security group inbound rules:

```text
22/tcp   from trusted admin IPs
80/tcp   from 0.0.0.0/0 and ::/0
443/tcp  from 0.0.0.0/0 and ::/0
```

DNS:

- Create or update the `A` record for your deployment hostname to the EC2 public IPv4 address.
- Create an `AAAA` record only if the instance has a working public IPv6 address.
- DNS can stay with the organization's DNS provider; it only needs to point to the AWS instance.

Install Docker and the Compose plugin on the host, then unpack:

```bash
sudo mkdir -p /opt/metrum-ai-router
sudo tar -C /opt/metrum-ai-router --strip-components=1 -xzf metrum-ai-router-<version>-docker-linux-amd64.tar.gz
cd /opt/metrum-ai-router
```

Use the `docker-linux-amd64` package on x86_64 hosts and the `docker-linux-arm64` package on ARM64 hosts.

## Compose package upgrade

First-time bootstrap may unpack with `tar --strip-components=1` as shown above. Upgrading an existing Compose install must use `scripts/compose_package_upgrade.py`. Do not hand-write a remote unpacker, do not glob-move from `/`, do not stop or reboot the host, and do not use EKS tooling for this path.

```bash
python3 scripts/compose_package_upgrade.py plan \
  --package dist/metrum-ai-router-<version>-docker-linux-amd64.tar.gz \
  --install-root /opt/metrum-ai-router

python3 scripts/compose_package_upgrade.py apply \
  --package dist/metrum-ai-router-<version>-docker-linux-amd64.tar.gz \
  --install-root /opt/metrum-ai-router \
  --backup-suffix <purpose> \
  --remote ubuntu@<compose-host> \
  --ssh-identity <ssh-key>
```

The script:

- unpacks with `tar --strip-components=1` into a staging directory, then replaces the install root;
- copies live `compose/config`, `compose/state`, `compose/logs`, `compose/.env`, and `compose/ROUTER_TOKEN*.txt` from the timestamped backup;
- restores container UID/GID `65532` on copied runtime directories;
- pins `SMART_LLMROUTER_VERSION` from the package filename;
- includes `docker-compose.postgres-localhost.yml` when `ROUTER_USAGE_DB_DSN` is set in `.env`;
- loads the packaged image and runs `docker compose config` plus `docker compose up -d`;
- prints only safe scalars (backup path, image tar name, compose service name/image/status).

Do not use this upgrade path to jump a Postgres usage database onto a package that requires `docs/DATA_MIGRATIONS.md` work unless that gate has already been completed while the router is stopped.

## Compose usage-store reset

When an existing Compose Postgres usage schema cannot be adopted and historical usage may be discarded, use `scripts/compose_clean_cutover.py`. Do not hand-write volume deletion, do not `docker compose down` with volumes, do not replace live `config.yaml` / `env.json` / `ROUTER_TOKEN*.txt`, and do not stop or reboot the host.

```bash
python3 scripts/compose_clean_cutover.py plan \
  --package dist/metrum-ai-router-<version>-docker-linux-amd64.tar.gz \
  --install-root /opt/metrum-ai-router

python3 scripts/compose_clean_cutover.py apply \
  --package dist/metrum-ai-router-<version>-docker-linux-amd64.tar.gz \
  --install-root /opt/metrum-ai-router \
  --backup-suffix <purpose> \
  --confirm-reset-usage reset-postgres-data \
  --remote ubuntu@<compose-host> \
  --ssh-identity <ssh-key>
```

The script:

- fail-closes unless live `compose/config/config.yaml`, `env.json`, `.env` with `ROUTER_USAGE_DB_DSN`, the Postgres override file, and `ROUTER_TOKEN*.txt` are present;
- `pg_dump`s the live usage database (custom format) and restic-archives it under stable paths/tags (`purpose:compose-usage-archive`, `version:<id>`). Credentials come from ignored `env.json` (`BACKUP_USER`, `BACKUP_PASS`, `RESTIC_PASSWORD`). The snapshot is the dump plus a safe scalar manifest; it does not include `env.json` or `config.yaml`;
- stops the router only, applies `scripts/compose_package_upgrade.py` with compose start skipped, and loads the packaged image;
- discovers the Compose `postgres_data` volume from `docker compose config`, removes only that volume, and recreates Postgres with the preserved `.env`;
- runs the PostgreSQL `docs/DATA_MIGRATIONS.md` gate (`plan` → `apply` → `resume` `historical-usage-validation-v1` until job state `validated` → `verify-serving` → `status`) with `--dsn-env=ROUTER_USAGE_DB_DSN` and never a DSN on the CLI;
- then `docker compose up -d` and prints only safe scalars.

Callers, model groups, provider keys, license/quota state, and Caddy volumes stay. Usage rows, reports, and the migration ledger do not. Package from a clean merged `origin/main` worktree only.

Serving rollback after a usage-store reset still needs `scripts/compose_package_upgrade.py rollback` plus a new empty Postgres volume and a re-run of the empty-DB migration gate. Restic restore of the usage dump is forensics, not an in-place schema downgrade.

Rollback of a package upgrade that did not reset the usage store:

```bash
python3 scripts/compose_package_upgrade.py rollback \
  --backup /opt/metrum-ai-router.backup-<purpose>-<UTC timestamp> \
  --install-root /opt/metrum-ai-router \
  --remote ubuntu@<compose-host> \
  --ssh-identity <ssh-key>
```

This runbook is source-only. Do not add it to `scripts/package_docs_allowlist.txt`.

## First install image and runtime

Load the packaged image:

```bash
docker load -i images/metrum-ai-router-<version>-linux-amd64.tar
```

Prepare runtime directories and config:

```bash
mkdir -p compose/config/scripts compose/state compose/logs
cp config/config.example.yaml compose/config/config.yaml
cp config/env.example.json compose/config/env.json
cp config/scripts/router.ts compose/config/scripts/router.ts
sudo chown -R 65532:65532 compose/config compose/state compose/logs
chmod 0750 compose/config compose/config/scripts
chmod 0400 compose/config/env.json
```

The router container runs as UID/GID `65532`. The config directory must be traversable by that ID, `env.json` must be readable by that ID, and the state/log bind mounts must be writable by that ID.

If the TypeScript routing script imports local helpers, copy those files into `compose/config/scripts/` as well. If it imports third-party packages, build and lock those dependencies before packaging and deploy either the required dependency tree under `compose/config/scripts/` or a pre-bundled script artifact. The container bundles scripts at router startup; it does not install npm packages at runtime.

## Routing Policy Design Checklist

Before exposing a deployment model group to users:

- Define the workload and owner.
- Choose whether this belongs in one model group, multiple groups, or a separate router instance.
- Choose the strategy: `static`, `failover`, `weighted`, `dynamic_score`, `script`, `external`, or a contract-backed combination.
- Define eligible providers and models under `models.<group>.targets[]`; do not treat provider catalog entries as active routes.
- Document required API shapes, tool modes, modalities, reasoning controls, structured-output support, and max-token cap behavior.
- Define quality, cost, latency, throughput, error-rate, timeout, and fallback targets.
- Run direct upstream smokes for every provider/model/dialect/skin being claimed.
- Run router-level smokes through each caller API shape and negative no-eligible-target path.
- Run representative evaluation or proof for the workload.
- Define rollback: remove the target from affected groups, remove or tighten the capability metadata that made it eligible, isolate it behind a restricted smoke group, relax a contract only when the contract is too strict, switch strategy, or restore the previous config.

The public customer-facing version of this ownership model is `docs-site/docs/routing/customer-controlled-routing.md`.

External routing-policy calls are opt-in per script model group with `script_http.enabled: true`, exact `allow_hosts`, `timeout_ms`, and `max_response_bytes`. Scripts call allowlisted services with `router.fetchJSON`; unrestricted `fetch`, runtime package installation, provider keys, and raw router tokens are not exposed to scripts. HTTPS is required for non-local services unless `script_http.allow_http: true` is explicitly approved; loopback HTTP is allowed for local demos and sidecars. Redirects must keep an allowed scheme and exact allowlisted hostname. Put policy-service auth in env-expanded `script_http.headers`, not in script source.

Each script decision uses a fresh VM. `script_max_concurrent` defaults to `16`
per script group and accepts values through `256`; requests waiting for a slot
honor cancellation. Start with the default, load-test the intended request
shape, and raise it only with CPU/memory evidence. Roll back by restoring the
previous cap or strategy. This cap is admission control, not a VM execution
timeout: once admitted, unbounded pure JavaScript can hold its slot and request
goroutine indefinitely. `router.fetchJSON` has a separate HTTP timeout. Keep
scripts bounded, and restart affected containers after restoring a stuck
policy.

For PII-aware script routing, package the policy file under `compose/config/scripts/`, mark private backing targets with deployment-owned metadata such as `tier: private` or `display_name: Private sensitive target`, and smoke-test that likely PII prompts select only those targets for primary routing and retry fallbacks. The `examples/typescript-pii-policy/` demo returns safe labels only, fails closed when no sensitive/private target is eligible, and does not redact outbound content. Use model-group `pii_filter` when the router must redact, restore, or fail requests before provider calls.

For standalone policy services, configure `strategy: external` with `external_policy.url`, exact `allow_hosts`, low `timeout_ms`, `max_response_bytes`, and config-owned auth headers. Use HTTPS unless the service is loopback-local or `external_policy.allow_http: true` is approved for a trusted internal endpoint. Redirects are blocked when any hop changes to a non-allowlisted hostname. The service receives safe routing context and eligible target metadata, then returns the selected target decision. The router reuses one HTTP client per group and propagates caller cancellation. Start new policies with `external_policy.mode: shadow`, promote to `enforce` only after evidence review, and roll back policy calls immediately with `mode: baseline`.

For `dynamic_score`, conversation affinity is enabled by default and stores
only a caller-isolated prefix digest plus selected target. Pins are
process-local, expire after the configured TTL, and never override current
request eligibility. Disable affinity without changing the scoring policy with
`routing_policy.dynamic_score.affinity.enabled: false`; restarting the
container also clears pins.

Edit `compose/config/config.yaml` for the default durable SQLite paths:

```yaml
server:
  listen: ":8080"
  logging:
    path: /app/logs/requests.jsonl
  usage_db:
    enabled: true
    driver: sqlite
    path: /app/state/usage.sqlite
    migration_policy: deployment-job

state_path: /app/state/router-state.json
```

The base Compose profile requires only a pinned `SMART_LLMROUTER_VERSION`, has no database credentials/TCP database egress, and persists `usage.sqlite` on `./state:/app/state`. Run the non-serving SQLite migration gate before `docker compose up`.

PostgreSQL is an explicit multi-replica or externally managed database choice. Include the packaged localhost-only override, set `POSTGRES_PASSWORD` and `ROUTER_USAGE_DB_DSN`, and configure `driver: postgres` with `dsn: ${ROUTER_USAGE_DB_DSN}`. The override binds Postgres to `127.0.0.1:${POSTGRES_HOST_PORT:-15432}`; do not publish it on `0.0.0.0`.

The response cache is in-memory inside the router container. Restarting the container clears cached responses. Cache hits are shared across caller tokens, return fresh router-owned response IDs, and do not consume provider credits or persisted caller token quota. Cache hit/miss/bypass, item count, occupied bytes, max bytes, and occupancy percentage are persisted per request in the usage DB.

Token-budget admission reserves estimated input tokens, tool/schema payload size, structured-output schema payload size, and the caller's requested output cap before upstream calls. The reservation uses `max_tokens`, `max_completion_tokens`, `max_output_tokens`, or the router-injected Messages default cap when applicable. TPM, daily token, monthly token, and lifetime key checks include in-flight reservations; request-count quotas are unchanged. Successful requests persist actual upstream-reported usage, and failures, cancellations, and cache hits release or avoid token reservations.

Caller traffic shaping is optional and disabled by default. Configure `server.traffic_shape.default_caller` for inherited defaults, or `callers[].traffic_shape` for one key. Caller config wins over the server default; `enabled: false` on a caller opts that key out of an enabled server default. Shaping is process-local token-bucket state and resets on router restart. It runs only after auth, model allow lists, and request token estimation, and before upstream calls. Request-start queueing occurs before the active concurrency slot is acquired so queued requests do not occupy `concurrent`; hard `rpm`, `tpm`, `concurrent`, quota, lifetime-budget, and license checks still run and cannot be bypassed. Request-start shaping still applies to cache hits; input/output/total reservation shaping runs only for cache misses that have passed hard token reservation and would otherwise call upstream. Queueing is disabled unless `queue.enabled: true` with positive `max_wait_ms` and `max_depth`.

Production-style bounded queue caller:

```yaml
callers:
  - id: example-coding-prod
    rate: { rpm: 240, tpm: 5000000, concurrent: 16 }
    traffic_shape:
      enabled: true
      request_start_per_sec: 2.0
      request_burst: 8
      input_tokens_per_sec: 75000
      input_token_burst: 300000
      output_reservation_tokens_per_sec: 60000
      output_reservation_token_burst: 250000
      total_reserved_tokens_per_sec: 120000
      total_reserved_token_burst: 500000
      queue:
        enabled: true
        max_wait_ms: 1500
        max_depth: 16
```

Roll out by enabling one non-critical caller first, sending a controlled burst with realistic `max_tokens`, and confirming brief bursts queue and complete within `max_wait_ms` while bursts beyond `max_depth` return deterministic `429 traffic-shaped` responses with `Retry-After`, `request_id`, and `bucket` without upstream attempts for rejected requests. Then compare shaped requests, queued count, queue wait p50/p95/max, upstream provider 429s, latency, cancellations, and user-visible errors in usage reports. Roll back by setting the caller `traffic_shape.queue.enabled: false` to keep fail-fast shaping, setting the caller `traffic_shape.enabled: false`, or setting `server.traffic_shape.enabled: false` for inherited defaults, then restart/reload through the normal config process and verify new queued events stop while hard quota behavior remains.

Upstream request safety is controlled under `server.upstream`. `timeout_ms` bounds the shared upstream HTTP client, `default_attempt_timeout_ms` provides a default per-target cap when no group or target override is set, and `max_response_bytes` bounds successful upstream bodies before decode or synthesized streaming. The router does not follow upstream API redirects. Image URL requests reject literal or initially resolved loopback, link-local, RFC1918/private, multicast, and unspecified destinations by default, with one bounded 500 ms DNS budget per request. The router forwards accepted image URLs without dereferencing or probing redirects, so deployment egress controls must independently block redirect targets and DNS rebinding to private, metadata, and administrative destinations.

Keep `allow_private_image_urls: false` unless a reviewed private VLM
deployment intentionally accepts the risk. Setting it to `true` bypasses the
router's entire image-URL admission step, including scheme, host/address, and
DNS checks; it is not a narrow RFC1918 exception. The provider may then
dereference any forwarded URL, so network egress policy must block metadata,
administrative, and other prohibited destinations and redirect/rebinding
paths.

Same-dialect OpenAI Chat and Anthropic Messages streaming is native upstream
SSE. Ensure Caddy or another ingress does not buffer event streams. Once the
first event is written, the router does not retry or replay the request to a
fallback target; caller cancellation cancels the upstream. OpenAI Responses
and cross-dialect bridges retain unary upstream behavior with router-encoded
caller streaming.

Provider/model/target `traffic_shape` blocks are optional and should be rolled out disabled or conservatively. For a first production rollout, enable a low-risk provider or smoke group, set small request-start and token bursts, run two parallel caller tokens through the same group, and verify routing either skips the shaped target or returns `503 upstream-capacity-throttled` with a safe request ID. Query `request_upstream_shape_events` for `admitted`, `skipped`, and `cooldown_started` decisions before broadening limits. Roll back by removing or disabling the `traffic_shape` block and restarting the router; restart also clears in-memory token buckets and adaptive backoff state.

Generate a caller token:

```bash
docker run --rm --entrypoint /app/bin/metrum-ai-router-token-gen metrum-ai-router:<version>-linux-amd64 generate \
  --owner-user alice \
  --project example-project \
  --env dev \
  --allow <allowed-model-group>[,<allowed-model-group>...]
```

Append the generated caller config to `compose/config/config.yaml`, add or verify the matching `users`, `projects`, and `project_memberships` entries, and save the printed `token` for clients.

Verify the caller-facing model groups with the same token before handing it to users:

```bash
curl -H "Authorization: Bearer $ROUTER_TOKEN" "$ROUTER_BASE_URL/v1/models"
```

The response is filtered by that token's `allow` entries. Every returned `id` is a router model group the caller can use in Chat Completions, Responses, Messages, Codex CLI, or Claude Code CLI. Missing groups require an allow-list update.

Review `compose/.env`:

```bash
SMART_LLMROUTER_VERSION=<version>-linux-amd64
ROUTER_HOSTNAME=your-router.example.com
CADDY_EMAIL=admin@example.com
CADDY_HTTP_PORT=80
CADDY_HTTPS_PORT=443
POSTGRES_DB=llmrouter
POSTGRES_USER=llmrouter
POSTGRES_PASSWORD=<strong-random-db-password>
ROUTER_USAGE_DB_DSN=host=postgres port=5432 user=llmrouter password=<strong-random-db-password> dbname=llmrouter sslmode=disable TimeZone=UTC
```

`SMART_LLMROUTER_VERSION` must match the image tag loaded from the package, such as `<version>-linux-amd64` or `<version>-linux-arm64`; it must not be `latest`.

Validate the rendered compose model before starting:

```bash
docker compose config >/dev/null
```

Before a fresh serving startup, run the mandatory [Data migration framework](DATA_MIGRATIONS.md) deployment-job gate: `plan`, approved backup, `apply`, every release-defined data job until `validated`, `verify`, then `status`. With `server.usage_db.migration_policy: deployment-job`, do not start the router until the final status is compatible/current. For PostgreSQL production, `auto-safe` is not a substitute for this non-serving job; checkpoint ordinal `0` is not completion evidence.

Start only after that gate:

```bash
cd /opt/metrum-ai-router/compose
docker compose up -d
```

Caddy terminates TLS and proxies to the private `router:8080` service. Caddy will obtain certificates automatically once DNS points to the instance and ports 80/443 are reachable.

## Smoke Test

From outside the instance:

```bash
export ROUTER_BASE_URL="https://your-router.example.com"
curl "$ROUTER_BASE_URL/healthz"
curl "$ROUTER_BASE_URL/version"
curl -H "Authorization: Bearer $ROUTER_TOKEN" "$ROUTER_BASE_URL/v1/models"
```

Inside the running container, `/app/bin/metrum-ai-router --version`, `/app/bin/metrum-ai-router-token-gen --version`, and `/app/bin/metrum-ai-router-usage-report --version` print the package version, commit, full UTC build timestamp, Go version, OS, and architecture. Hosted browser docs display the package version and build timestamp on every page and return `X-Smart-LLMRouter-*` version headers.

The router image also embeds authenticated admin report assets, including the Metrum-branded browser shell, local logo/font assets, JavaScript, and chart bundle. They are disabled by default and served only under `/admin/reports/` after `server.admin_reports.enabled: true`, Basic Auth, and Casbin `admin:reports` policy are configured. Optional security access reports require `server.admin_reports.security.enabled: true`, trusted proxy configuration under `server.client_ip`, and separate `admin:security_reports` policy. Public `/docs/` remains separate from report data.

Normal Docker release builds require an operator-generated `license.json` and
configured verification public key. Mount runtime inputs under
`compose/config/`, configure the license and state paths, and restart the
router. Keep the private signing key outside the compose tree. Renewal is an
atomic file replacement plus restart or recheck; rollback restores the previous
valid license/public-key pair.

Runtime licensing was removed in 3.0.0. Customer-facing hosted docs no longer cover license installation or renewal as a product requirement.

If a customer receives a license through an approved commercial/control-plane delivery flow, the Docker installation steps do not change: place the issued `license.json` under the protected compose config directory, restart or wait for recheck, and validate `/readyz`, safe admin status, and one caller workflow. The router container must not receive payment-provider secrets, card data, signing-service private credentials, or full commercial back-office records.

Enterprise deployments can route to private vLLM or SGLang services by configuring them as OpenAI-compatible providers with internal `/v1` base URLs. Before adding those targets to active production groups, validate the upstream `/v1/models` ID, a direct text completion, any required tool-call path, and the same requests through the router. Record model IDs, vLLM/SGLang parser flags, chat templates, server versions, and rollback steps in deployment notes. See `docs/SELF_HOSTED_UPSTREAMS.md`.

Generate a markdown usage report on the host from the running compose data:

```bash
dsn="$(sed -n 's/^ROUTER_USAGE_DB_DSN=//p' .env | tail -n 1)"
docker compose run --rm --entrypoint /app/bin/metrum-ai-router-usage-report router \
  --driver postgres \
  --dsn "$dsn" \
  --since 24h \
  --out /app/logs/usage-24h.md
```

Add `--caller-user`, `--caller-project`, `--caller-environment`, `--token-id`, `--token-id-prefix`, `--resolved-group`, or `--client` to narrow a report to one owner user, benchmark, caller cohort, model group, or CLI client.

The report includes internal router API key usage by `token_id`/owner user/project/environment, caller IP usage, hourly usage by caller IP, external provider/model calls, token totals, request-time USD cost, cache hit/miss/bypass, cache occupancy, attempts, fallbacks, status codes, latency, hourly usage, daily usage, downstream user performance, upstream provider/model/dialect performance, and per-request upstream/downstream tokens/sec. Use the downstream user and upstream endpoint performance sections first when triaging slow UX. It does not include raw router tokens or provider API keys.

Usage rows, throughput fields, request-time pricing/cost fields, and cache snapshots are durable across restarts when the Postgres volume is preserved. The in-memory response cache and `/metrics` process counters reset when the router restarts. `/metrics` is global operational telemetry and is only available to caller subjects authorized for `metrics` `read`; existing `metrics_admin: true` callers remain compatible through generated Casbin grants. Normal application keys should use `/v1/usage` or usage reports.

For browser report rollout, smoke `/admin/reports/`, `/admin/reports/api/version`, and `/admin/reports/api/summary?since=24h` with an authorized browser-admin user, verify the Metrum-branded dark shell and version chip load, confirm ordinary router tokens receive `403 reports-forbidden`, and disable reports by setting `server.admin_reports.enabled: false` if rollback is needed. Also smoke `/admin/reports/?tab=requests&since=24h&limit=50&sort=timeUtc&direction=desc`; verify the table shows current-page range text, Next updates the URL with `cursor=<next_cursor>`, changing a filter resets the cursor, server-sortable headers update `sort` and `direction`, and `CSV current page` exports only the returned request rows. At the API layer, confirm `/admin/reports/api/requests?since=24h&limit=50&sort=timeUtc&direction=desc` has `pagination.mode: "cursor"`, `returned <= 50`, no secret fields, and no duplicated request IDs when fetching `cursor=<next_cursor>`. Smoke `/admin/reports/export.md?since=24h&limit=50` and verify it is bounded to recent request rows; smoke `/admin/reports/export.md?since=24h&limit=50&mode=summary` and verify it reports full-window SQL totals plus top-N aggregate sections without raw request IDs or token secrets. Pick one recent safe request ID and smoke `/admin/reports/api/request-evidence?request_id=<request_id>`; verify `diagnosticCompleteness`, `evidenceSections`, stored request-time cost fields, attempt rows when attempts happened, `Cache-Control: no-store`, Casbin domain scoping, and no raw prompts, images, tool schemas, tool outputs, tokens, token hashes, provider keys, upstream bodies, cookies, OIDC tokens, full config, or private paths. For an aggregate such as `/admin/reports/?tab=usage-by-key&since=24h&limit=50`, confirm the browser labels it as top-N and does not expose cursor navigation; at the API layer, confirm `/admin/reports/api/usage-by-key?since=24h&limit=50` returns `pagination.mode: "top_n"` so operators understand the table is a ranked bounded summary. If security reports are enabled, smoke `/admin/reports/api/security/events?since=24h&limit=50`, verify an admin without `admin:security_reports` receives `403 reports-forbidden`, confirm security events also return cursor pagination metadata, and verify CSV export contains no raw tokens, token hashes, provider keys, prompts, images, cookies, OIDC tokens, or full config.

For traffic tuning advisor rollout, smoke both browser and CLI paths before changing any shaping config:

```bash
curl -fsS -u admin:<password> \
  "$ROUTER_BASE_URL/admin/reports/api/traffic-tuning-advisor?since=24h&limit=50"

router-usage-report \
  --driver postgres \
  --dsn "$ROUTER_USAGE_DB_DSN" \
  --since 24h \
  --traffic-tuning-advisor
```

Verify the advisor returns only safe scalar evidence and recommendation/config-field hints, not raw prompts, images, tool schemas, bearer tokens, token hashes, provider keys, or full config. For a known heavy coding-agent user, compare the advisor with Traffic Shaping, Provider Capacity Shaping, Upstream Failures, Request Shape Failures, and Requests. If recommendations indicate `route_around_incompatible_target`, roll out by changing target eligibility or route weights, not burst/queue values. If recommendations indicate burst or queue changes, make one scoped caller/server change, restart or reload through the normal process, rerun controlled burst and normal-request smokes, then compare queue wait p50/p95/max, router 429 count, upstream 429/5xx, latency, and client cancellations. Roll back by restoring the previous config backup, disabling the caller queue, disabling the caller `traffic_shape`, or disabling inherited `server.traffic_shape.enabled`, depending on the changed field.

Never remove a PostgreSQL volume for a migration, upgrade, rollback, repair, or production reset. An empty confirmed non-production reset is the only possible destructive-volume case, after independent confirmation of environment, volume identity, and no retained data; it is intentionally outside this production runbook. Use the backup/restore and deployment-job sequence in [Data migration framework](DATA_MIGRATIONS.md) instead.

Example hosted/reference deployment model groups. These names and weights are deployment-defined examples, not product-required constants:

```text
default    Baseten GPT OSS 120B 51%, MiniMax-M3 27%, Kimi K2.7 Code 6%, Baseten GLM 5.2 5%, Crusoe GLM 5.2 5%, Baseten Nemotron 3%, OpenRouter Gemma 4 26B Nitro 2%, OpenAI GPT-5.4 Nano 1%.
fast       Baseten GPT OSS 120B 56%, MiniMax-M3 26%, Kimi K2.7 Code 5%, Baseten GLM 5.2 5%, Baseten Nemotron 3%, OpenRouter Gemma 4 26B Nitro 2%, Crusoe GLM 5.2 2%, OpenAI GPT-5.4 Nano 1%.
small      Baseten GPT OSS 120B 58%, MiniMax-M3 28%, Kimi K2.7 Code 4%, Baseten Nemotron 3%, Baseten GLM 5.2 2%, OpenRouter Gemma 4 26B Nitro 2%, Crusoe GLM 5.2 2%, OpenAI GPT-5.4 Nano 1%.
medium     Baseten GPT OSS 120B 51%, MiniMax-M3 25%, Kimi K2.7 Code 8%, Baseten GLM 5.2 5%, Crusoe GLM 5.2 5%, Baseten Nemotron 3%, OpenRouter Gemma 4 26B Nitro 2%, OpenAI GPT-5.4 Nano 1%.
high       Baseten GPT OSS 120B 45%, MiniMax-M3 26%, Kimi K2.7 Code 10%, Crusoe GLM 5.2 7%, Baseten GLM 5.2 6%, Baseten Nemotron 3%, OpenRouter Gemma 4 26B Nitro 2%, OpenAI GPT-5.4 Nano 1%; ordinary non-MiniMax targets are request-shape capped at 1 MiB until gt-1mb OpenAI Chat tool payloads pass target-specific validation.
big-coder  Reasoning-capable code-heavy route: Fireworks GPT OSS 20B 25%, MiniMax M3 Responses 25%, xAI Grok 4.5 15%, Fireworks DeepSeek-V4-Flash 15%, MiniMax M3 Chat 5%, Kimi K2.7 Code 5%, Crusoe GLM 5.2 5%, and OpenAI GPT-5.4 Nano 5% for ordinary text and compatible Chat/Responses traffic; Fireworks GPT OSS 20B and xAI Grok 4.5 handle Chat reasoning and Anthropic-thinking translation, MiniMax M3 Responses handles Responses reasoning, Fireworks Responses Kimi K2.7 Code remains available for Codex/Responses tool traffic, and MiniMax/Kimi Anthropic-compatible targets remain available for Claude Code-style tool traffic. Request-shape metadata can further filter those weights; production-derived opencode/AI SDK Chat `stream_options` requests are gated away from incident-backed Chat targets until exact smokes pass.
```

Caller tokens are restricted by `callers[].allow`. Model group names are deployment-defined; the names above are examples from this hosted/reference deployment. `/v1/models` only lists model groups allowed for the presented token, and disallowed requests return `403 model-not-allowed` before any upstream provider call.

Claude Code:

```bash
unset ANTHROPIC_API_KEY
export ANTHROPIC_BASE_URL="$ROUTER_BASE_URL/anthropic"
export ANTHROPIC_AUTH_TOKEN="$ROUTER_TOKEN"
claude --bare --print --model "<allowed-model-group>" "Reply with exactly: router claude ok"
```

Codex:

```bash
export METRUM_ROUTER_KEY="$ROUTER_TOKEN"
umask 077
curl -fsS "$ROUTER_BASE_URL/v1/codex/models.json" \
  -H "Authorization: Bearer $METRUM_ROUTER_KEY" \
  -o "$HOME/.codex/metrum-models.json"
codex exec --ignore-user-config --ephemeral \
  --ignore-rules \
  --skip-git-repo-check \
  -c 'model="<allowed-model-group>"' \
  -c 'model_provider="metrum-ai-router"' \
  -c 'model_catalog_json="~/.codex/metrum-models.json"' \
  -c 'model_providers.metrum-ai-router.name="Metrum AI Router"' \
  -c 'model_providers.metrum-ai-router.base_url="'"$ROUTER_BASE_URL"'/v1"' \
  -c 'model_providers.metrum-ai-router.env_key="METRUM_ROUTER_KEY"' \
  -c 'model_providers.metrum-ai-router.wire_api="responses"' \
  "Reply with exactly: router codex ok" </dev/null
```

The `exec` subcommand is required for `--ignore-user-config`, `--ephemeral`, `--ignore-rules`, and `--skip-git-repo-check`; those flags are not accepted by the top-level interactive `codex` command.

Interactive Codex uses top-level `codex`, without the `exec`-only flags:

```bash
export METRUM_ROUTER_KEY="$ROUTER_TOKEN"
codex \
  -c 'model="<allowed-model-group>"' \
  -c 'model_provider="metrum-ai-router"' \
  -c 'model_catalog_json="~/.codex/metrum-models.json"' \
  -c 'model_providers.metrum-ai-router.name="Metrum AI Router"' \
  -c 'model_providers.metrum-ai-router.base_url="'"$ROUTER_BASE_URL"'/v1"' \
  -c 'model_providers.metrum-ai-router.env_key="METRUM_ROUTER_KEY"' \
  -c 'model_providers.metrum-ai-router.wire_api="responses"'
```

Tool-capable acceptance checks should exercise the real agent tool paths, not just text echo. Run tool-client smokes only inside a disposable container image that contains the required `claude` and `codex` CLIs. The container should receive only the router base URL and a scoped router token, bind-mount only a scratch smoke directory, drop Linux capabilities, set CPU/memory/PID limits, and avoid mounting the operator home directory, SSH keys, provider-key files, source checkout, or production config. Tool-bearing requests are not cacheable, because their results depend on shell/filesystem/tool state.
Use `claude-tools-smoke-openrouter` and `agent-tools-smoke-openrouter` when the acceptance gate must specifically validate OpenRouter's Anthropic-compatible and Responses-compatible tool routes.
For OpenAI Chat Completions agent clients such as Warp Agent, run a `/v1/chat/completions` smoke with `tools`, `tool_choice`, `stream: true`, and a realistic agent User-Agent. The router should preserve the OpenAI Chat tool payload, select a target with explicit `tool_support.openai_chat`, and stream back `delta.tool_calls` plus `finish_reason: "tool_calls"`.
For self-hosted vLLM or SGLang tool routes, first run an OpenAI-compatible chat `tools` request directly against the upstream, then route the same request through the configured router model group. Tool support depends on the model, parser, chat template, streaming mode, and `tool_choice` mode.

Claude Code tool smoke pattern:

```bash
unset ANTHROPIC_API_KEY
mkdir -p /tmp/router-claude-tool-smoke
docker run --rm --network host --cap-drop ALL --security-opt no-new-privileges \
  --cpus 1 --memory 1g --pids-limit 256 --read-only \
  --tmpfs /tmp:rw,nosuid,nodev,size=256m \
  --mount type=bind,source=/tmp/router-claude-tool-smoke,target=/workspace \
  -e "ANTHROPIC_BASE_URL=$ROUTER_BASE_URL/anthropic" \
  -e "ANTHROPIC_AUTH_TOKEN=$ROUTER_TOKEN" \
  -w /workspace "$TOOL_SMOKE_IMAGE" \
  claude --bare --print --model claude-tools-smoke \
    --permission-mode bypassPermissions \
    --allowedTools "Write,Bash" \
    "Create claude_tool_smoke.txt containing exactly claude-tool-ok, run cat claude_tool_smoke.txt, then finish with claude-tool-ok."
test "$(cat /tmp/router-claude-tool-smoke/claude_tool_smoke.txt)" = "claude-tool-ok"
```

Codex tool smoke pattern:

```bash
mkdir -p /tmp/router-codex-tool-smoke
curl -fsS "$ROUTER_BASE_URL/v1/codex/models.json" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -o /tmp/router-codex-tool-smoke/metrum-models.json
docker run --rm --network host --cap-drop ALL --security-opt no-new-privileges \
  --cpus 1 --memory 1g --pids-limit 256 --read-only \
  --tmpfs /tmp:rw,nosuid,nodev,size=256m \
  --mount type=bind,source=/tmp/router-codex-tool-smoke,target=/workspace \
  -e "METRUM_ROUTER_KEY=$ROUTER_TOKEN" \
  -e "ROUTER_BASE_URL=$ROUTER_BASE_URL" \
  -w /workspace "$TOOL_SMOKE_IMAGE" \
  codex exec --ignore-user-config --ephemeral \
    --ignore-rules \
    --skip-git-repo-check \
    --dangerously-bypass-approvals-and-sandbox \
    -C /workspace \
    -c 'model="agent-tools-smoke"' \
    -c 'model_provider="metrum-ai-router"' \
    -c 'model_catalog_json="/workspace/metrum-models.json"' \
    -c 'model_providers.metrum-ai-router.name="Metrum AI Router"' \
    -c "model_providers.metrum-ai-router.base_url=\"${ROUTER_BASE_URL}/v1\"" \
    -c 'model_providers.metrum-ai-router.env_key="METRUM_ROUTER_KEY"' \
    -c 'model_providers.metrum-ai-router.wire_api="responses"' \
    "Create codex_tool_smoke.txt containing exactly codex-tool-ok, run cat codex_tool_smoke.txt, then finish with codex-tool-ok." </dev/null
test "$(cat /tmp/router-codex-tool-smoke/codex_tool_smoke.txt)" = "codex-tool-ok"
```

## Local Compose E2E

For local e2e, run Caddy on plain HTTP by setting:

```bash
ROUTER_HOSTNAME=:80 CADDY_HTTP_PORT=18080 CADDY_HTTPS_PORT=18443 docker compose up -d
```

Then use:

```text
http://127.0.0.1:18080
```

`scripts/compose_live_e2e.sh` runs its Claude and Codex tool smokes with the operator or CI machine's installed CLI clients. The clients target the disposable Compose router URL and write only in disposable E2E work directories; neither CLI is copied into the router image or required on the Compose target. The Claude smoke unsets direct Anthropic credentials and uses its router bearer token. Codex runs with its `workspace-write` sandbox, so its operator/CI host must permit unprivileged user namespaces; the harness exits before building the target when that prerequisite is unavailable. The compose e2e temp config directory is private, secret files are written `0600`, and a Docker helper changes only the bind-mounted config tree to owner UID/GID `65532` so the packaged router can read `/app/config/config.yaml` and `/app/config/env.json` through the read-only `./config:/app/config:ro` mount without making them world-readable. Override `COMPOSE_E2E_PERMISSIONS_IMAGE` if the default lightweight helper image is not available. Retained workdirs scrub `config/env.json` and `.env`, then restore bind-mounted ownership before removal or handoff.
