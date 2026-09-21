# Metrum AI Router Deployment

Source-only operator runbook. Do not add this file to `scripts/package_docs_allowlist.txt`; package-safe external bootstrap deployment guidance belongs in `docs/PACKAGE_README.md`, `docs/BINARY_INSTALL.md`, `docs/DOCKER_COMPOSE_INSTALL.md`, and the router-served Docusaurus installation docs.

This project is packaged as a binary distribution. A deployment host does not need the Go toolchain, Node.js, Docusaurus, or source tree. Release binaries embed the customer-facing Docusaurus documentation and serve it under `/docs/`; browser requests to `/` redirect there.

## Package Contents

`make package` creates Linux x86_64 and arm64 tarballs under `dist/`:

```text
metrum-ai-router-<version>-linux-amd64.tar.gz
metrum-ai-router-<version>-linux-arm64.tar.gz
```

Each tarball contains:

```text
bin/metrum-ai-router
bin/metrum-ai-router-token-gen
bin/metrum-ai-router-usage-report
bin/metrum-ai-router-migrate
bin/metrum-ai-routerctl
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
caddy/Caddyfile
```

Keep the four root legal files together during publication and redistribution.
`LICENSE` covers first-party content under Apache-2.0; `NOTICE` carries
distributed notices; `THIRD_PARTY_NOTICES.md` records dependency and asset
terms and surfaces; and `MODEL_LICENSES.md` records model and dataset term
boundaries. Review unresolved entries for the exact artifact rather than
assuming the first-party license grants third-party rights. Leftover
`license.json` files are inert in 3.0.0+ and must not be added to the package
legal set.

Package docs are an explicit Tier 2 bootstrap allowlist maintained in `scripts/package_docs_allowlist.txt`. Internal production runbooks, source-maintenance notes, and troubleshooting notes with private hostnames, SSH paths, live compose paths, router token files, or provider-key material must stay out of release packages. Full external admin guidance belongs in the embedded Docusaurus docs served under `/docs/`.

Release package targets require a clean git tree and reject versions containing `-dirty`. Commit the intended code, generated embedded docs, and admin assets before building customer release artifacts. For a local development artifact that will not be shipped, set `ALLOW_DIRTY_PACKAGE=1` explicitly:

```bash
ALLOW_DIRTY_PACKAGE=1 make package-one-no-docs
```

All package tar commands run with `COPYFILE_DISABLE=1` so macOS does not inject AppleDouble `._*` metadata. `scripts/validate_package_contents.py` rejects AppleDouble entries, unexpected files, missing allowlisted docs, internal runbooks, local secret/state filenames, private production markers, raw token/provider-key patterns, and binary-package ELF architecture mismatches. The validation step is part of each package target and must pass before publishing an artifact.

Release metadata is intentionally strict because package and Docker recipes use it in paths, tags, and linker flags. `VERSION`, `COMMIT`, `BUILD_DATE`, `GOOS`, `GOARCH`, `PKG_NAME`, `DIST_DIR`, `IMAGE_NAME`, and `IMAGE_TAG` must pass `scripts/validate_build_metadata.py` before package or image commands run. Versions may use ordinary `git describe` characters such as letters, digits, `.`, `_`, `+`, `/`, and `-`; shell metacharacters, empty values, absolute paths, and `..` path components are rejected.

The config and routing script are packaged together so this command works after unpacking:

```bash
bin/metrum-ai-router --config config/config.yaml
```

`config/config.yaml` can keep `script: scripts/router.ts` because script paths are resolved relative to the config file.

If `router.ts` imports local helpers, place those files under `config/scripts/` and include them in the release package. If it imports third-party packages, install and lock them before packaging and ship either the resolved dependency tree needed by esbuild or a pre-bundled script artifact. The router bundles from the deployment filesystem at startup and does not run `npm install` on the host.

External TypeScript policy calls are disabled unless a script model group enables `script_http` in `config.yaml`. Configure exact `allow_hosts`, a small `timeout_ms`, and `max_response_bytes`; scripts call these services through `router.fetchJSON`, not unrestricted browser `fetch`. HTTPS is the default for non-local services. Plain HTTP is allowed only for loopback hosts or when `script_http.allow_http: true` is set for trusted internal infrastructure. Redirects are revalidated at every hop against the same scheme and exact-host rules. Put policy-service auth in `script_http.headers` with env-expanded values such as `${ROUTING_POLICY_AUTH_HEADER}` instead of hardcoding secrets in script source.

Each script decision runs in a fresh isolated VM. Keep the per-group
`script_max_concurrent` at its default `16` until load evidence justifies a
change; accepted values are `0`/unset through `256`, where zero means the
default. Requests queued for a VM slot honor cancellation. Increasing the cap
increases simultaneous script CPU/memory work inside the serving process, so
roll back by restoring the prior cap or strategy rather than bypassing the
limit. The cap does not impose an execution timeout after a VM starts:
unbounded pure JavaScript can hold its slot and request goroutine indefinitely.
`router.fetchJSON` has a separate bounded HTTP timeout, but it does not preempt
other script execution. Review scripts for bounded loops and roll back a stuck
policy by restoring the previous artifact and restarting affected instances.

Quota state files are tamper-evident runtime state. Keep the state directory private, copy state files through normal backup/restore procedures, and do not edit JSON counters or disabled flags by hand; integrity failures are treated as serving errors until a trusted backup restores state. Legacy unsigned quota-state import requires an explicit one-time start with `METRUM_AI_ROUTER_ALLOW_UNSIGNED_STATE_MIGRATION=1` (legacy alias `SMART_LLMROUTER_ALLOW_UNSIGNED_STATE_MIGRATION=1` still accepted), then a restart without that flag after signed state is written.

For PII-aware script routing, mark private backing targets with deployment-owned metadata such as `tier: private` or `display_name: Private sensitive target`, and test that likely PII requests select only those targets for both primary routing and retry fallbacks. The example in `examples/typescript-pii-policy/` returns safe class labels only, does not log or return matched text, and fails closed when no sensitive/private target is eligible. Script routing does not redact outbound content; use model-group `pii_filter` for router-managed redaction, restoration, or fail-on-match controls.

For standalone policy services, prefer `strategy: external` with
`external_policy.url`, exact `allow_hosts`, low `timeout_ms`, response-size
limits, and config-owned auth headers. The service receives derived request
context plus pseudonymous caller identity and eligible-target deployment
inventory, returns `targetIndex` or `target`, and is validated before any
upstream provider call. It does not receive prompt text, message bodies, image
URLs/data, tool schemas, tool outputs, or `request.raw` unless
`external_policy.include_request: true` is explicitly approved for a trusted
service. Use HTTPS unless the service is loopback-local or
`external_policy.allow_http: true` is explicitly approved for a trusted
internal endpoint. Redirects cannot escape the allowlist. The router reuses
one concurrency-safe HTTP client per external group and propagates caller
cancellation. Start in `mode: shadow`, promote to `enforce` only after
recommendation-versus-served evidence passes, and roll back policy influence
with `mode: baseline`, which skips the policy call. See
`docs/EXTERNAL_ROUTING_POLICY.md`.

For built-in adaptive routing, prefer `strategy: dynamic_score` before adding custom strategies. It scores only the requested model group's eligible targets, uses in-memory rolling observations instead of hot-path database reads, emits safe scalar `routing_decision` traces, and pins a caller/conversation prefix to the selected target by default. Pins are process-local, TTL-bounded, and ignored when the target is no longer eligible. Disable only pinning with `routing_policy.dynamic_score.affinity.enabled: false`; roll back the strategy by switching the group to `weighted`. See `docs/DYNAMIC_SCORE_ROUTING.md`.

For optional `targets[].region` diagnostics, choose a stable deployment-owned
taxonomy only after validating the actual provider/infrastructure location;
the label is not inferred or enforced. Before rollout, plan/apply usage
migration `2026090901`, validate YAML, smoke both the primary and a forced
successful fallback, and confirm `request_usage.target_region` and safe
selected-target telemetry identify the target that actually served. The value
is not exposed to callers, TypeScript policy, or external-policy input. Roll
back by removing the config field and preserving the additive column; a
package downgrade follows the migration contract and may require restoring
the approved pre-migration database.

Runtime licensing was removed in 3.0.0. Leftover `license.json` files are
inert (never read). Legacy `server.license` config is accepted with one startup
warning in 3.0.0 and rejected in 4.0.0. Do not mount or issue a license as a
deployment prerequisite.

Docker Compose packages are built separately:

```bash
make package-docker
```

Docker packages use `docker buildx build --platform linux/<arch> --load`, save exactly one image tar for the package architecture, and run the same package-content validation as binary packages. Validate both release families before handoff:

```bash
make package-all
make package-docker-all
python3 scripts/validate_package_contents.py --allowlist scripts/package_docs_allowlist.txt dist/metrum-ai-router-*.tar.gz
```

Bind the complete four-artifact set to the approved version, full commit ID,
and one UTC build timestamp before upload. This reruns package validation and
writes deterministic `dist/SHA256SUMS` plus `dist/release-artifacts.json` with
filename, byte size, SHA-256, package family, platform, validation result, and
reproducible commands:

```bash
export VERSION=vX.Y.Z
export COMMIT="$(git rev-parse HEAD)"
export BUILD_DATE=YYYY-MM-DDTHH:MM:SSZ
make package-all package-docker-all
make release-artifact-inventory
(cd dist && sha256sum --check SHA256SUMS)
```

Use the same three metadata values for both build commands. The inventory
fails unless exactly one binary and one Docker package exist for each of
`linux/amd64` and `linux/arm64`. Keep these local files as candidate evidence;
publishing them, adding release URLs, signing a tag, and creating the public
GitHub Release remain release-authority actions. After publication, download
every asset into an empty directory, run `sha256sum --check SHA256SUMS`, and
record the public URL and result in the release evidence.

Release archival uses restic against an operator-configured backup repository.
Set `RESTIC_REPOSITORY` or `RESTIC_REPO_HOST` plus `RESTIC_REPO_PATH` (with
`BACKUP_USER`/`BACKUP_PASS`) in ignored `ops.env.json`; there are no compiled-in
host or path defaults. `make package-all` produces the release binary packages.
`make package-docker-all` produces the shared customer Docker packages. After both
families exist under `dist/`:

```bash
# Credentials in ignored ops.env.json: BACKUP_USER, BACKUP_PASS, RESTIC_PASSWORD,
# and RESTIC_REPOSITORY or RESTIC_REPO_HOST + RESTIC_REPO_PATH
make dist-backup
```

Local artifacts keep the git version/hash in their filenames
(`metrum-ai-router-<version>-linux-amd64.tar.gz`, …). The backup stages them
under stable restic basenames so each snapshot replaces the same four paths:

- `metrum-ai-router-linux-amd64.tar.gz`
- `metrum-ai-router-linux-arm64.tar.gz`
- `metrum-ai-router-docker-linux-amd64.tar.gz`
- `metrum-ai-router-docker-linux-arm64.tar.gz`

Release identity is recorded in restic tags (`version:<id>`). Restore a prior
build with `restic snapshots --tag version:<id>` then
`restic restore <snapshot-id>`.

Or build both families and archive in one step:

```bash
make package-dist-backup
```

Use `docs/DOCKER_DEPLOYMENT.md` when deploying the packaged Docker image tarball plus Caddy compose stack to AWS EC2 or a similar host. Use `docs/DEPLOYMENT_PATTERNS.md` when choosing between evaluation-hosted, self-hosted central, per-environment, per-team, hierarchical/federated, and private managed topologies.

Kubernetes examples are maintained under `deploy/kubernetes/`. They are Kustomize-friendly raw manifests with placeholder-only Secret examples, an external Postgres DSN, `/readyz` and `/healthz` probes, ingress, network policy, a state PVC, and a PDB. The base kustomization does not apply `secret.example.yaml`; operators must create real Secrets through the deployment secret-management process first. Before using the manifests in production, operators must push the per-architecture package image to a deployment-owned registry, replace every placeholder, review the network policy against the cluster CNI, and smoke `/readyz`, `/docs/`, `/v1/models`, one caller request, and admin reports if enabled. Keep the example at one router replica unless the selected state/quota design has been validated for horizontal scaling.

For NVIDIA local-serving (in-cluster vLLM + router, KV cache off by default), use
`deploy/kubernetes/overlays/nvidia-local-serving/` and
`metrum-ai-routerctl blueprint render` from
`deploy/kubernetes/intents/shadeform-nvidia-local-models.example.yaml` (L40S
fallback) or `shadeform-nvidia-local-models-b200.example.yaml` (B200/H200).
Offline gate: `make test-k8s-nvidia-local-serving`. Live Shadeform steps:
`docs/SHADEFORM_NVIDIA_LOCAL_SERVING_E2E.md`.

For an operator-selected llm-d frontend with a vLLM backend, use the separate
`nvidia-llmd-compat` blueprint profile and
`deploy/kubernetes/intents/shadeform-nvidia-llmd-compat.example.yaml`. Metrum AI
Router sends OpenAI-compatible traffic to `llm-d-local-epp:8081`; it does not
control llm-d replica selection. Offline gate:
`make test-k8s-nvidia-llmd-compat`. Live/operator procedure:
`docs/SHADEFORM_NVIDIA_LLMD_COMPAT_E2E.md`.

For operator-controlled k3s clusters on AMD Instinct, use the manual
`deploy/kubernetes/overlays/k3s-amd-instinct-local-serving/` vLLM/ROCm
manifests. The router remains GPU-free and reaches only the in-cluster serving
Services. Offline gate: `make test-k8s-amd-instinct-local-serving`. Live on-prem
steps: `docs/K3S_AMD_INSTINCT_LOCAL_SERVING_E2E.md`. Detailed Dell/AMD-aligned
architecture, routing policy, and observability design:
`docs/amd-instinct-local-serving-reference-architecture.md`. The manual path is
intentional until the blueprint CLI ships an AMD profile; do not relabel or
apply the NVIDIA serving overlay on AMD nodes.

The AMD overlay is an integration example, not evidence that every AMD SKU,
driver, ROCm release, serving image, model, parser, or request shape is
supported. Its offline gate checks manifest structure and security invariants;
record hardware-backed direct-upstream and router smokes before making a
deployment-specific compatibility claim.

Generic Kubernetes samples remain under
`deploy/kubernetes/base/` and `deploy/kubernetes/overlays/example/`. Historical
staging Make delivery notes are retired (archived 2026-09-01); do not revive
those targets for live mutation.

## Example Deployment Host

Use a deployment-owned hostname for the router, for example:

```text
router.example.com
```

The service is externally reachable, but model and usage endpoints require a valid router caller token, and `/metrics` requires a caller token authorized for `metrics` `read`. Existing `metrics_admin: true` caller config is converted to equivalent Casbin grants at startup. Optional browser admin reports under `/admin/reports/` require browser-admin identity plus Casbin authorization and are disabled by default. Caddy terminates TLS and reverse-proxies to the router on localhost.

Use the DNS provider for the deployment environment. Create or update an `A` record pointing to the public IPv4 address of the deployment host. Add an `AAAA` record only if the host has working public IPv6.

## Host Layout

Recommended paths:

```text
/opt/smart-llmrouter/bin/metrum-ai-router
/opt/smart-llmrouter/bin/metrum-ai-router-token-gen
/opt/smart-llmrouter/config/config.yaml
/opt/smart-llmrouter/config/env.json
/opt/smart-llmrouter/config/scripts/router.ts
/var/lib/smart-llmrouter/router-state.json
/var/log/smart-llmrouter/requests.jsonl
/etc/caddy/Caddyfile
```

Use `env.json` or a deployment secret manager for provider API keys on the deployment host. The packaged `env.example.json` contains empty placeholders only; copy it as a shape template, then populate the protected runtime `env.json` or inject the same variables from the service environment. Keep runtime `env.json` mode `0600` and do not place it in a web-served path.

The response cache is process-local. Restarting the router clears cached responses. Cache hits are shared across caller tokens, return fresh router-owned response IDs, and do not consume provider credits or persisted caller token quota. Cache telemetry is durable because each usage row stores cache hit/miss/bypass, item count, occupied bytes, max bytes, and occupancy percentage.

Token quotas are admitted with an in-flight reservation before upstream calls. The reservation is estimated input tokens, tool/schema payload size, structured-output schema payload size, and the caller's requested output cap (`max_tokens`, `max_completion_tokens`, or `max_output_tokens`) or the router-injected Messages default cap when applicable. TPM, daily token, monthly token, and lifetime key checks include other in-flight reservations; request-count quotas remain request based. Completed requests persist actual upstream-reported usage, while failures, cancellations, and cache hits release or avoid token reservations.

## Install

On the deployment host:

```bash
sudo mkdir -p /opt/smart-llmrouter /var/lib/smart-llmrouter /var/log/smart-llmrouter
sudo tar -C /opt/smart-llmrouter --strip-components=1 -xzf metrum-ai-router-<version>-linux-amd64.tar.gz
sudo cp /opt/smart-llmrouter/config/config.example.yaml /opt/smart-llmrouter/config/config.yaml
sudo cp /opt/smart-llmrouter/config/env.example.json /opt/smart-llmrouter/config/env.json
sudo chmod 0755 /opt/smart-llmrouter/bin/metrum-ai-router /opt/smart-llmrouter/bin/metrum-ai-router-token-gen
sudo chmod 0600 /opt/smart-llmrouter/config/env.json
```

Use the `linux-amd64` tarball for x86_64 hosts and the `linux-arm64` tarball for ARM64 hosts.

Edit `/opt/smart-llmrouter/config/config.yaml`:

```yaml
server:
  listen: "127.0.0.1:8080"
  logging:
    path: /var/log/smart-llmrouter/requests.jsonl
  usage_db:
    enabled: true
    driver: sqlite
    path: /var/lib/smart-llmrouter/usage.sqlite

state_path: /var/lib/smart-llmrouter/router-state.json
```

New packaged Docker Compose installations also default to SQLite at `/app/state/usage.sqlite` on the durable `./state:/app/state` bind. Set `server.logging.path: /app/logs/requests.jsonl`, `server.usage_db.path: /app/state/usage.sqlite`, `server.usage_db.migration_policy: deployment-job`, and `state_path: /app/state/router-state.json` before the non-serving migration gate. The base Compose profile requires only `SMART_LLMROUTER_VERSION`; it has no database credentials or TCP database egress.

PostgreSQL is an explicit choice for multi-replica or externally managed database deployments. Include `docker-compose.postgres-localhost.yml`, configure `server.usage_db.driver: postgres` and `dsn: ${ROUTER_USAGE_DB_DSN}`, and supply `POSTGRES_PASSWORD`/`ROUTER_USAGE_DB_DSN`. The override binds Postgres only to `127.0.0.1:${POSTGRES_HOST_PORT:-15432}`.

Edit `/opt/smart-llmrouter/config/env.json` with provider keys such as `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `GROQ_API_KEY`, `MOONSHOT_API_KEY`, `MINIMAX_API_KEY`, and `XAI_API_KEY`, or provide those variables through the host's secret manager or service environment. Do not copy real values back into `env.example.json`; `make secret-check` fails if tracked env examples contain live-looking keys.

Generate a caller token and append the generated caller block to `config.yaml`:

```bash
/opt/smart-llmrouter/bin/metrum-ai-router-token-gen generate \
  --owner-user alice \
  --project example-project \
  --env dev \
  --allow <allowed-model-group>[,<allowed-model-group>...]
```

Save the printed `token` value for the client. Add or verify the referenced `users`, `projects`, and `project_memberships` entries before restart; every key must reference an active owner user, project, and membership. The router config stores only `token_sha256` and `token_id`. Each account id, caller `id`, `token_sha256`, and non-empty `token_id` must be unique after normalization; token hashes are checked case-insensitively. User, project, and membership statuses support `active`, `disabled`, `suspended`, `removed`, and `archived`; caller key statuses also support `expired` and `rotated`.

Use `--allow` to restrict each generated key to specific internal model groups. Model group names are deployment-defined; any names shown in examples are reference deployment names only. `/v1/models` only lists model groups allowed for the presented token, and disallowed requests return `403 model-not-allowed` before any upstream provider call.

Caller model discovery is part of the access contract. After issuing or rotating a token, verify the caller-facing list with the same token:

```bash
curl -H "Authorization: Bearer $ROUTER_TOKEN" "$ROUTER_BASE_URL/v1/models"
```

Every listed `id` is a router model group the caller can request. Missing groups require an allow-list change, not a client-side workaround.

## systemd

Create `/etc/systemd/system/smart-llmrouter.service`:

```ini
[Unit]
Description=Metrum AI Router
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=smart-llmrouter
Group=smart-llmrouter
WorkingDirectory=/opt/smart-llmrouter
ExecStart=/opt/smart-llmrouter/bin/metrum-ai-router --config /opt/smart-llmrouter/config/config.yaml
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/smart-llmrouter /var/log/smart-llmrouter

[Install]
WantedBy=multi-user.target
```

Create the service user and start the service:

```bash
sudo useradd --system --home /opt/smart-llmrouter --shell /usr/sbin/nologin smart-llmrouter
sudo chown -R smart-llmrouter:smart-llmrouter /opt/smart-llmrouter /var/lib/smart-llmrouter /var/log/smart-llmrouter
sudo systemctl daemon-reload
sudo systemctl enable --now smart-llmrouter
```

## Caddy TLS Termination

Install Caddy on the host and place the packaged `caddy/Caddyfile` at `/etc/caddy/Caddyfile`.

The Caddyfile terminates TLS for the configured deployment hostname and proxies to `127.0.0.1:8080`. Caddy obtains and renews public certificates automatically after DNS points the hostname to the host and ports `80` and `443` are reachable.

Reload Caddy:

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

## Smoke Test

From a client machine:

```bash
export ROUTER_BASE_URL="https://your-router.example.com"
curl "$ROUTER_BASE_URL/healthz"
curl "$ROUTER_BASE_URL/version"
curl -H "Authorization: Bearer $ROUTER_TOKEN" "$ROUTER_BASE_URL/v1/models"
```

On the host, `bin/metrum-ai-router --version`, `bin/metrum-ai-router-token-gen --version`, `bin/metrum-ai-router-usage-report --version`, and `bin/metrum-ai-router-migrate --version` print the package version, commit, full UTC build timestamp, Go version, OS, and architecture. Hosted browser docs display the package version and build timestamp on every page and return `X-Smart-LLMRouter-*` version headers.

If enabling admin browser reports, first deploy with `server.admin_reports.enabled: false`, then add Basic Auth or OIDC, Casbin policy for `admin:reports`, and finally enable `server.admin_reports.enabled: true`. Smoke `/admin/reports/`, `/admin/reports/api/version`, `/admin/reports/api/summary?since=24h`, `/admin/reports/api/requests?since=24h&limit=25`, `/admin/reports/api/request-evidence?request_id=<request_id>`, `/admin/reports/api/provider-catalog-status`, and `/admin/reports/api/retention-status` with an authorized browser-admin user, verify the Metrum-branded dark shell and version chip, verify ordinary router tokens receive `403 reports-forbidden`, verify `/docs/` remains public docs only, and roll back by setting `server.admin_reports.enabled: false`. Report data is scoped to the admin's Casbin domain by default; smoke a domain-scoped admin against another project/environment request ID and expect `404`. Grant deployment-global report visibility only with an explicit `*` policy domain and smoke that subject separately. Check that report filters and request evidence return bounded safe rows, diagnostic completeness, stored request-time cost fields, and no raw tokens, token hashes, provider keys, prompts, images, image URLs, tool schemas, tool outputs, cookies, OIDC tokens, upstream bodies, full config, or private host paths. If an admin report API returns `report-query-failed`, keep the caller-facing error generic and inspect the matching `admin_report_query_failed` router log for safe scalar fields such as `report`, `handler`, `db_driver`, `error_class`, and, for PostgreSQL, `pg_code`, `pg_severity`, and bounded `pg_message`. If enabling security access reports, also configure `server.client_ip.trusted_proxy_cidrs`, set `server.admin_reports.security.enabled: true`, grant `admin:security_reports` read/export only to approved subjects, smoke `/admin/reports/api/security/events?since=24h`, and verify exports contain no raw tokens, token hashes, provider keys, prompts, images, cookies, OIDC tokens, full config, or raw spreadsheet formulas. For metrics/report authorization rollout, use `server.admin_auth.authorization.source: static` with `policy_file` or inline `policy`, or `source: db` after a policy set has been validated and activated in the usage DB. Smoke `/metrics` with an authorized caller, verify ordinary callers still receive `403 metrics-forbidden`, and keep a known-good static policy file or retired DB policy set for rollback.

Enterprise deployments can route to internally hosted vLLM or SGLang services by configuring them as OpenAI-compatible providers with private `/v1` base URLs. Validate the upstream `/v1/models` ID, a direct text completion, any intended tool-call path, and the same requests through the router before activating those targets in production groups. Keep model IDs, parser flags, chat templates, server versions, and rollback notes in deployment records. See `docs/SELF_HOSTED_UPSTREAMS.md`.

Use the same base URL for local CLIs:

```bash
unset ANTHROPIC_API_KEY
export ANTHROPIC_BASE_URL="$ROUTER_BASE_URL/anthropic"
export ANTHROPIC_AUTH_TOKEN="$ROUTER_TOKEN"

export METRUM_ROUTER_KEY="$ROUTER_TOKEN"
```

Codex provider base URL:

```text
https://your-router.example.com/v1
```

## Release E2E

Full release e2e is live and credit-consuming:

```bash
make e2e-live-full
make e2e-compose-live
```

These checks require live provider keys, actual local Claude Code and Codex CLIs, Docker, and Docker Compose. They verify real provider calls, CLI traffic through the router, Docker Compose startup, and Caddy proxying.
