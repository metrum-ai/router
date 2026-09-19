---
title: Release Notes
doc_type: reference
---

# Release Notes

Release notes help customer operators decide whether to deploy, how to validate the release, and how to roll back if needed. Each packaged router build also displays its router version and build timestamp on every docs page.

For upgrade execution, see [Upgrade Guide](/docs/release-notes/upgrade-guide). For the docs package index, see [Releases](/docs/releases).

The entries below describe the validation contract for the package that embeds
this page. The version banner, `/docs/releases`, and `/version` are the
authoritative sources for its exact router version and build timestamp; do not
infer the running version from a date written in documentation.

## v2.2.0 - 2026-09-19

### Highlights

- Caller-facing identifiers can be rewritten with AES-SIV so upstream IDs are
  not exposed on the wire, including field-level rewrite on native SSE (#205).
- Incremental SSE translation for same-dialect Responses and for Chat ↔
  Responses bridges (#206–#208).
- Incremental Anthropic ↔ OpenAI Chat/Responses text and tool streaming (#209);
  reasoning remains on native Anthropic routes.
- LRP synthetic CI no longer requires host AppArmor profile loads, so privileged
  Docker self-hosted runners can complete sandbox verification (#210).

### Operator Impact

| Area | Change |
| --- | --- |
| Config | Optional `server.identifiers` (`rewrite` / `passthrough`) and transform key material; default streaming translator remains `incremental` |
| Streaming | Bridge paths emit incremental SSE when eligible; `server.streaming.translator: synthesized` keeps unary upstream behavior |
| Database | No new usage migration in this release |
| Packages | Canonical `metrum-ai-router*` binaries unchanged from v2.1.0 naming |
| CI / runners | Self-hosted Docker runner pools that run LRP synthetic need privileged containers for bubblewrap; AppArmor host profiles are not required |

### Caller Impact

- Streaming Chat ↔ Responses and Anthropic ↔ OpenAI text/tool bridges return
  incremental SSE instead of failing closed when the bridge is enabled and
  `server.streaming.translator` is `incremental`.
- When identifier rewrite is enabled, caller-visible IDs are transformed;
  `passthrough` preserves prior exposure behavior for lab/mock configs.
- Reasoning workloads should continue to use native Anthropic routes; these
  bridges do not synthesize thinking blocks.

### Upgrade

1. Download `metrum-ai-router-v2.2.0-linux-<arch>.tar.gz` (and Docker package if
   used) from the GitHub Release; verify against `SHA256SUMS` /
   `release-artifacts.json`.
2. Review `server.identifiers` and `server.streaming.translator` before enabling
   rewrite or changing translator mode in production.
3. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide) for Compose /
   Kubernetes procedures.

### Validation

- `make api-compat-mock-offline`
- `make sse-capture` (or package SSE harness checks from source)
- `make test` and `make lrp-test` when exercising LRP packages from source
- After deploy: `/readyz`, `/version` reports v2.2.0; smoke streaming bridges
  and confirm identifier rewrite or passthrough matches the reviewed config

### Rollback

Roll back to GitHub Release **v2.1.0** (`metrum-ai-router-*` artifacts). No
usage-database restore is required for this release. Revert identifier and
streaming config knobs with the prior package if rewrite or incremental bridges
were enabled.

## v2.1.0 - 2026-09-14

### Highlights

- Usage evidence can persist optional cached-input token counts and
  `cached_input_price_per_million_usd`, and can partition input cost when both
  cache evidence and a known cached-input price are present (#160).
- Learned Routing Policy gains offline seed-corpus tooling (#157), a
  multi-provider coding portfolio (OpenRouter Qwen + MiniMax, Fireworks kimi,
  Baseten GLM), a routing-benchmark harness (#159), and pluggable embedder
  backends with explicit CUDA/CPU device selection (#156 A/C).
- LRP serving/eval/signed-bundle gaps from #158 are closed. Public seed and
  benchmark evidence scalars are checked in under
  `docs/evidence/learned-routing-policy/`.

### Operator Impact

| Area | Change |
| --- | --- |
| Config | Optional `cached_input_price_per_million_usd` on catalog/target pricing |
| Database | Usage migration `2026091301` (schema version 5); `restore-required` rollback |
| LRP | Opt-in only; new seed/benchmark/embedder tooling does not auto-enable enforce |
| Packages | Canonical `metrum-ai-router*` binaries unchanged from v2.0.0 naming |

### Upgrade

1. Download `metrum-ai-router-v2.1.0-linux-<arch>.tar.gz` (and Docker package if
   used) from the GitHub Release; verify against `SHA256SUMS` /
   `release-artifacts.json`.
2. Apply usage migration `2026091301` per
   [DATA_MIGRATIONS.md](https://github.com/metrum-ai/router/blob/main/docs/DATA_MIGRATIONS.md)
   with an approved pre-migration backup (restore-required).
3. If using LRP, sync the locked uv project, refresh target pricing descriptors,
   and keep router `external_policy.mode` in `shadow` until workload gates pass.
4. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide) for Compose /
   Kubernetes procedures.

### Validation

- `python3 scripts/validate_package_contents_test.py`
- `python3 scripts/release_artifact_inventory_test.py`
- `make test` and `make lrp-test` when exercising LRP packages from source
- After deploy: `/readyz`, `/version` reports v2.1.0; confirm nullable cached
  input columns on new usage rows when providers report cache reads

### Rollback

Roll back to GitHub Release **v2.0.0** (`metrum-ai-router-*` artifacts). Because
`2026091301` is `restore-required`, restore the approved pre-migration usage DB
snapshot before deploying the earlier package. Disable new LRP knobs or revert
to the prior LRP bundle if enforce was enabled.

## v2.0.0 - 2026-09-12

### Highlights

- Breaking packaging rename to the canonical technical slug
  **`metrum-ai-router`**. Packages and the runtime image ship **canonical
  binaries only**; rename stub binaries are not packaged.
- Release archives, Docker image tags, and GitHub Release artifact names use
  `metrum-ai-router-*` / `metrum-ai-router:<tag>`.
- Go module path remains `github.com/metrum-ai/router`.

### Operator Impact

| Area | Change |
| --- | --- |
| Runtime binary | `metrum-router` → `metrum-ai-router` |
| Token / usage / migrate CLIs | `metrum-router-*` → `metrum-ai-router-*` |
| Customer CLI | `metrum-routerctl` → `metrum-ai-routerctl` |
| Fleet / license / lifecycle CLIs | `metrum-genai-smartrouter-*` / `metrum-genai-customer-lifecycle` → `metrum-ai-router-fleetctl`, `metrum-ai-router-fleet-sign`, `metrum-ai-router-license`, `metrum-ai-router-customer-lifecycle` |
| Packaged stubs | Removed (`router`, `smartrouterctl`, `metrum-fleetctl`, …) |
| Archives / image | `metrum-router-*` → `metrum-ai-router-*` |
| Client `model_provider` examples | `metrum-router` → `metrum-ai-router` |
| Docs URL | Still `https://llm-api.apps.metrum.ai/docs` until docs.metrum.ai is hosted |

### Upgrade

1. Download `metrum-ai-router-v2.0.0-linux-<arch>.tar.gz` (and Docker package if used)
   from the GitHub Release; verify against `SHA256SUMS` / `release-artifacts.json`.
2. Update service unit, Compose image name, Kubernetes image references, and PATH
   installs to the new binary names. Do not rely on packaged rename stubs.
3. Update client configs that set `model_provider="metrum-router"` to
   `metrum-ai-router`.
4. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide) for migration
   gate and Compose/Kubernetes procedures.

### Validation

- `python3 scripts/validate_package_contents_test.py`
- `python3 scripts/release_artifact_inventory_test.py`
- `python3 scripts/canonical_product_test.py`
- After deploy: `/readyz`, `/version` reports v2.0.0, binaries on PATH are
  `metrum-ai-router*`, `/docs/` names Metrum AI Router

### Rollback

Roll back to GitHub Release **v1.4.4** (`metrum-router-*` artifacts and image
tags) with the prior service unit / Compose / Kubernetes references. Restore
config and usage DB backups taken before the upgrade if any migration ran.

## v1.4.4 - 2026-09-11

Packaged republish of the canonical **Metrum AI Router** naming commit as
GitHub Release `v1.4.4`. Prefer these release assets for deploy. The `v1.4.3`
tag/ref was locked by repository immutable-release rules after the initial
asset-upload failure; product content matches the v1.4.3 notes below.

## v1.4.3 - 2026-09-11

### Highlights

- Public docs, site title, `/docs/llms.txt`, and the branding lint now use
  **Metrum AI Router** as the only current product name. "LLM smart router"
  remains the category description, not a second product title.
- Example configs and admin-auth docs align the Basic-auth realm example with
  the runtime default (`Metrum AI Router Admin`).

### Operator Impact

- No runtime config schema, strategy name, metric, CLI flag, or binary rename.
- Technical identifiers (`metrum-router`, `smartrouterctl`, Helm chart IDs,
  Kubernetes labels) are unchanged.
- Admin Basic-auth realm default was already `"Metrum AI Router Admin"`.

### Upgrade

Standard image upgrade. No configuration migration is required for this release.

### Validation

- `make docs-qa && make docs-build`
- `make test`
- After deploy: `/readyz`, `/version` reports v1.4.3, `/docs/` and
  `/docs/llms.txt` name Metrum AI Router

### Rollback

Redeploy the previous package/image (`v1.4.2`). No database or config rollback
is required for this documentation/branding release.

## v1.4.2 - 2026-09-11

Packaged republish of the merged smart-router positioning commit as GitHub Release
`v1.4.2`. Prefer these release assets for deploy when remaining on that tag. The
`v1.4.1` tag/ref was locked by repository immutable-release rules after the
initial asset-upload failure; product content matches the v1.4.1 notes below.

## v1.4.1 - 2026-09-11

### Highlights

- Public positioning leads with Metrum AI Router as an open-source LLM smart
  router. Gateway functions remain available underneath routing rather than as
  the leading product classifier.
- Learned Routing Policy is linked from the public routing strategy table and
  decision tree, with checked-in synthetic holdout figures labeled as
  non-promotable wiring evidence.
- Offline `make proof-routing` proves one `dynamic_score` group can select
  different upstreams for trivial vs complex fixtures via request-evidence.
- Docs embed `/docs/llms.txt` for machine-readable product classification.

### Operator Impact

- No runtime config schema, strategy name, metric, CLI flag, or binary rename.
- Package docs allowlist is unchanged; `llms.txt` ships in the embedded
  Docusaurus tree.

### Upgrade

Standard image upgrade. No configuration migration is required for this release.

### Validation

- `make docs-qa && make docs-build`
- `make proof-routing`
- `make test`
- After deploy: `/readyz`, `/version`, `/docs/`, `/docs/llms.txt`

### Rollback

Redeploy the previous package/image. No database or config rollback is required
for this documentation/positioning release.

## v1.2.0 - 2026-09-09

### Highlights

- Learned Routing Policy follow-ups (#21–#37) ship as opt-in LRP and external
  policy enhancements. Defaults preserve v1.1.0 cheapest-above-floor behavior.
  No live routing activation is authorized by installing this release.
- External policy context adds a pseudonymous `conversationKey`, opt-in
  post-completion feedback callbacks, and governed verifier-hint metadata for
  Chat, Responses, and Anthropic (#21–#23).
- LRP adds operator-signed Ed25519 bundles, read-only usage-DB explore import,
  near-duplicate evaluation splits, serving-provider variance reports, SQL and
  allowlisted plugin verifiers, and protected human-judge audit sampling
  (#29, #31–#34, #37).
- Selection constraints cover upstream latency gates, per-project quality
  floors, evidence-based prompt-cache cost estimates, and bounded explanation
  labels (#26, #27, #35, #36).
- Uncertainty abstention, Thompson sampling, PSI/embedding drift
  recommendations, and evidence-backed cold-start exploration are available
  behind explicit LRP group knobs (#24, #25, #28, #30).

### Operator Impact

- Review external-policy docs for conversation key, feedback URL, and
  verifier-hint configuration. Feedback never sends prompts, tools, or
  credentials.
- LRP operators can enable signed-bundle trust stores, usage import, eval
  split options, selection constraints, and uncertainty/explore knobs through
  LRP YAML and CLI. Cryptography for signing pins to a patched 50.x release.
- Synthetic wiring and local e2e evidence passed for these surfaces. They do
  not establish provider-backed quality, production latency, or live deployment
  readiness. Keep new knobs off until deployment-owned validation completes.
- No usage schema migration is introduced by this release beyond contracts
  already documented for v1.1.0.

### Caller Impact

- Ordinary Chat, Responses, and Anthropic callers continue to send model-group
  requests. New conversation-key and verifier-hint fields are operator-gated
  metadata; callers do not choose LRP internals.
- Class labels may use bounded abbreviations such as `lrp:caf:q0.90:c2`. Coarse
  Prometheus labels stay low-cardinality. Rich explanations remain on
  authenticated LRP `/explain`.

### Validation

- `/readyz` and `/version`: confirm `v1.2.0` and the package build timestamp.
- Browser docs: confirm the v1.2.0 release notes and LRP follow-up pages.
- `make lrp-test` and `make lrp-e2e` when exercising LRP packages from the
  source archive.
- Completion smoke for any deployment that enables new external-policy or LRP
  knobs; keep synthetic wiring separate from provider-backed evidence.

### Rollback

- Restore the previous router package and previous reviewed config/license.
- Disable new LRP and external-policy knobs or revert to the prior LRP bundle.
- No reverse migration is required for this release’s LRP follow-ups.

## v1.1.0 - 2026-09-09

### Highlights

- Release binaries use Go 1.26.8, including standard-library security fixes
  identified by the package vulnerability scan.
- Learned Routing Policy (LRP) adds a separate Python/uv service that trains
  calibrated quality and output-token models and selects the cheapest eligible
  target predicted to meet a workload's quality floor. Training, evaluation,
  bundle validation, authenticated inference and explanation are documented in
  the [worked case study](/docs/evaluation/learned-routing-case-study).
- The LRP deadline defaults to 200 ms and accepts 1–4,500 ms through YAML or
  `lrp serve --deadline-ms`; allow additional headroom in the router timeout.
- Same-dialect OpenAI Chat and Anthropic Messages requests proxy native
  upstream SSE, including tool and usage events. Responses and cross-dialect
  streaming remain router-encoded after a unary upstream call.
- `dynamic_score` adds caller-isolated, process-local conversation affinity
  with bounded fixed-TTL storage.
- TypeScript decisions use fresh VMs with per-group concurrency admission;
  external policy adds explicit `baseline`, `shadow`, and `enforce` modes with
  reusable cancellation-aware HTTP clients.
- The outbound Gemini `generateContent` codec supports evidence-gated unary
  text targets. It is not a caller endpoint and does not enable tools, images,
  reasoning, structured output, or streaming.
- Targets may declare a bounded diagnostic `region` label. It records the
  actual serving target but does not enforce or prove processing location.
- Fleet lifecycle implementation is isolated from the serving request-path
  package, and package validation excludes Fleet binaries from runtime images.
- A second preregistered fixed-model OCR outcome gate demonstrates
  scalar-only quality, latency, cost, and regression decisions.

### Operator Impact

- LRP is opt-in trusted deployment infrastructure using `strategy: external`.
  Install its locked uv project from the v1.1.0 source archive, following the
  [LRP guide](/docs/routing/learned-routing-policy). Router binary and Docker
  packages do not start a policy service or contain trained bundles. No provider
  or model group is activated by installing this release.
- Synthetic training and routing evidence passed quality checks, but the
  controlled example's cost delta versus its anchor was only 0.685% and
  **failed** the cost promotion gate — that percentage is a failed-gate
  benchmark artifact, not a product or launch savings claim. Real BGE tests
  failed the embedding/full-feature budgets at 512 tokens on the shared test
  host. Default one-thread median full-feature latency exceeded 200 ms; size
  deadlines for the intended workload and expect fallback while timed-out
  inference retains its bounded worker slot. These results do not establish
  production quality, concurrency capacity, HTTP/router latency, or expected
  customer savings.
- Config: review `dynamic_score.affinity`, `script_max_concurrent`,
  `external_policy.mode`, and optional `targets[].region`. New external
  policies should begin in `shadow`; `baseline` skips policy calls.
- Database: migration `2026090901` adds non-null text
  `request_usage.target_region` with an empty default.
- Streaming: reverse proxies must not buffer SSE. After the first native event,
  a later failure cannot change the committed `200`, append an error envelope,
  or fall back.
- Security: `allow_private_image_urls: true` bypasses all router image-URL
  admission and therefore requires independent egress controls.
- Script safety: concurrency limits do not preempt unbounded JavaScript after
  a VM starts.

### Caller Impact

- Native Chat/Messages streams can deliver lower time-to-first-event and
  preserve upstream event shapes. Clients must treat a missing terminal event
  as incomplete.
- Native PII-filtered streams preserve placeholders; buffered responses and
  router-generated Responses/bridge streams can restore them.
- Existing model-group authorization and request-shape eligibility remain
  authoritative. Affinity and external policy cannot widen access.

### Validation

- LRP validation records 79 Python tests with zero skips, 11 real-binary/HTTP
  synthetic checks and 526 Go tests. Sanitized training, inference and benchmark
  evidence is linked from the case study. Before enabling enforcement, run real
  workload quality/cost gates and staging shadow validation for your targets.
- Run the documented native Chat and Messages text/tool/usage/cancellation
  smokes, plus Responses and bridge regressions.
- Exercise affinity hit, miss, expiry, ineligible replacement, caller
  isolation, restart, and multi-replica behavior.
- Load-test TypeScript admission/cancellation and compare external-policy
  `shadow` recommendation with the served baseline before promotion.
- Plan, apply, and verify migration `2026090901`; confirm serving-target region
  attribution after successful fallback.
- Keep Gemini targets catalog-only unless the exact provider/model/account has
  direct and restricted router text evidence.

### Rollback

- Disable affinity or restore the previous strategy; set external policy to
  `baseline`; restore the prior script/cap; remove Gemini or region-bearing
  targets from active groups; and restore the prior package/config.
- Native-stream rollback requires the prior package. Follow the migration
  contract before downgrading; restore the approved pre-migration database
  when the contract requires it.

## v1.0.2 - 2026-09-08

Metrum AI Router v1.0.2 is a documentation packaging release. Caller-facing
routing, model groups, and API behavior are unchanged from v1.0.1.

### Highlights

- Embedded Docusaurus docs now link the navbar GitHub control and Source License
  footer entry to the public Apache-2.0 home at
  `https://github.com/metrum-ai/router`.
- The legal software-licenses page uses the same public repository LICENSE URL.

### Operator Impact

- Config: unchanged.
- Database: no schema or data migration is introduced by this release.
- License: unchanged.
- Metrics and reports: unchanged.

### Caller Impact

- API behavior: unchanged.
- Model groups: unchanged.
- Errors: unchanged.
- Docs: `/docs/` GitHub and LICENSE links point at the public repository.

### Validation

- `/readyz` and `/version`: confirm `v1.0.2` and the new build timestamp.
- `/docs/`: confirm navbar and footer GitHub hrefs use
  `github.com/metrum-ai/router` and do not use
  `sysadmin-metrum-ai`.
- Completion smoke: run one request for an actively used model group.

### Rollback

- Restore the previous router package (`v1.0.1`) and the previous reviewed
  config.
- No reverse migration is required; preserve the usage database.

## v1.0.1 - 2026-09-08

Metrum AI Router v1.0.1 is a maintenance release focused on admin browser
report availability. Caller-facing routing, model groups, and API behavior are
unchanged from v1.0.0.

### Highlights

- Deployments that require admin browser reports can now declare the
  reverse-proxy networks in front of the router. A deployment whose
  `server.admin_auth.basic.trusted_proxy_cidrs` does not cover those networks is
  rejected before rollout instead of serving an admin surface that refuses every
  correct credential.
- Admin authentication documentation explains the failure mode where Basic Auth
  returns `401` for a valid password because the forwarded-HTTPS check runs
  before the password comparison.

### Operator Impact

- Config: when `allow_insecure_http` is `false`, confirm that
  `server.admin_auth.basic.trusted_proxy_cidrs` contains the network your reverse
  proxy or ingress controller connects from. In Kubernetes this is the cluster
  pod network, which usually differs from a local kind or Docker bridge range.
- Database: no schema or data migration is introduced by this release.
- License: unchanged.
- Metrics and reports: `/metrics` isolation and admin report authorization are
  unchanged.

### Caller Impact

- API behavior: unchanged.
- Model groups: unchanged.
- Errors: unchanged.

### Validation

- `/readyz` and `/version`: confirm the new router version and build timestamp.
- Admin reports: an unauthenticated request returns `401` with a Basic challenge,
  an incorrect password returns `401`, and an authorized request loads the report
  shell and a SQL-backed summary.
- Completion smoke: run one request per actively used model group.

### Rollback

- Restore the previous router package and the previous reviewed config.
- No reverse migration is required; preserve the usage database.

## v1.0.0 - 2026-09-07

Metrum AI Router v1.0.0 is the first public open-source release package.
Use the docs banner, `/docs/releases`, and `/version` for the exact router
version and build timestamp of the package that embeds this page.

### Highlights

- The package embeds documentation for its own runtime build and exposes the
  same version and build timestamp through the docs banner and `/version`.
- Caller-facing routing supports deployment-defined model groups across the
  configured OpenAI Chat, OpenAI Responses, and Anthropic Messages surfaces.
- Capability and request-shape eligibility keep tools, images, reasoning,
  structured outputs, output caps, bridges, and large payloads on targets
  validated for those exact surfaces.
- Usage, diagnostics, cost, latency, attempt, fallback, traffic-shaping, and
  governed admin-report surfaces use safe scalar operational evidence.
- Operations: `metrum-genai-smartrouterctl` is available for customer-local safe config,
  token-file, license, model, and aggregate-usage operations. Fleet lifecycle
  authority moved to binary-package-only `metrum-genai-smartrouter-fleetctl`; the old
  `metrum-fleetctl` / `metrum-smartrouterctl` / `smartrouterctl` names are
  one-release rename notices.
- Self-hosted serving: validated NVIDIA and AMD Instinct reference paths cover
  private in-cluster OpenAI-compatible model servers while keeping the router
  workload GPU-free.

### Operator Impact

- Config: compare the packaged `config.example.yaml` with the reviewed runtime
  config. Do not copy sample provider/model routes directly into production.
- Database: follow the package's migration policy and release-specific
  deployment record. The current migration contract is: `2026071901` creates
  schema version 1/data version 0 through an online explicit baseline;
  `2026072301` advances schema version 2/data version 0 and is
  `restore-required`; `2026080501` advances data version 1 and is also
  `restore-required` and requires its restart-safe
  `historical-usage-validation-v1` job to reach `validated` before service
  startup. Run the non-serving deployment-job gate and retain the approved
  pre-migration backup when required.
- License: verify the installed license permits the enabled features and
  deployment shape.
- Credentials: preserve the protected provider environment file or
  secret-manager state; never place provider keys in release evidence.
- Metrics and reports: retain `/metrics` isolation for metrics-admin subjects
  and verify report authorization after upgrade.
- CLI packaging: confirm customer Docker images include `metrum-genai-smartrouterctl` and
  exclude `metrum-genai-smartrouter-fleetctl`. Fleet RDS mutation remains fail-closed pending
  recorded non-production evidence and a qualified reviewer's approval, which a
  single-maintainer deployment may supply itself. Managed production profile
  authority is operator-owned and is not granted by the customer package.

### Caller Impact

- API behavior: validate every caller API skin and client workflow affected by
  the package or config change.
- Model groups: callers must discover their allowed deployment-defined groups
  through authenticated `/v1/models`.
- Errors: preserve structured caller-facing error types and request IDs; use
  sanitized attempt and diagnostic rows for root-cause analysis.
- Client compatibility: run the actual Codex and Claude Code CLIs when routing,
  tools, images, auth, or model metadata changed.

### Compatibility and Known Limitations

- Model groups are deployment-defined; callers discover their allowed groups
  through authenticated `/v1/models` rather than relying on fixed names.
- Capability eligibility is request-shape-specific. A target is used only for
  dialects, tools, modalities, bridges, and payload sizes validated for it.
- AMD and NVIDIA local-serving support depends on the exact operator, driver,
  serving image, model, parser, hardware, and request shape documented by the
  deployment's validation evidence.
- The docs build retains an unpatched build-time image metadata dependency risk.
  It is not executable in the published browser bundle. The v1.0.0 launch
  decision accepts this risk through 2026-10-04 with reviewed documentation
  inputs, bounded build jobs, and static generated docs as the serving boundary;
  the accountable owner must recheck or remediate it by that date.

### Validation

- Confirm the docs banner, `/docs/releases`, and `/version` agree on the
  expected version and build timestamp for the deployed package.
- Run `/readyz`.
- Run authenticated `/v1/models` with each affected caller class.
- Run representative Chat, Responses, Messages, streaming, tool, image, and
  output-cap smokes for every changed surface.
- Run metrics-admin and ordinary-caller `/metrics` authorization checks when
  metrics are enabled.
- Run admin report and license-status checks when those features are enabled.

### Rollback

- Restore the previous package and reviewed runtime config.
- Restore the previous license input only when the license changed.
- Never run a reverse migration. Preserve the current usage database only when
  the release contract allows package/config rollback without restore; for a
  `restore-required` contract, restore the approved pre-migration snapshot
  before deploying the earlier package.
- Repeat `/readyz`, `/version`, `/v1/models`, and the failed caller/client smoke
  before returning traffic.
