# Changelog

All notable changes to Metrum AI Router are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `server.responses.model_identity: requested_group` makes caller-facing
  responses report the requested router model group (for example
  `model: big-coder`) instead of the serving upstream model. It covers Chat
  Completions, Responses and Anthropic Messages, unary and streaming, bridges,
  fallback and response-cache hits (#254, #255, #256).
- In `requested_group` mode, upstream failure errors no longer name providers
  or upstream models: `error.details.targets` becomes `{attempt, error_class}`,
  `last_error` is cut to the status line with provider, host and model names
  replaced by `[upstream]`, and `target_dialect` is omitted (#257).
- The default stays `model_identity: upstream`, which keeps today's response
  bytes. Usage rows, attempt rows, logs and admin reports keep the upstream
  provider and model in both modes.
- `make local-router` enables `requested_group`, so the local `big-coder`
  instance reports `model: big-coder`. `make local-router-pi-smoke` runs a pi
  write/read tool continuation against a running instance and checks the
  reported model and usage rows (#258).
- The api-compat suite now has offline contracts for 35 of the 43 rows that
  were `blocked` in the #94 catalog (Chat, Anthropic, Responses, routing,
  streaming, HTTP, media and ops). Remaining gaps stay `blocked` with a
  rationale (`make api-compat-mock`). Harbor HARBOR-01 runs inside its task container,
  and a router-only pi Harbor adapter is in `examples/harbor-algotune-pca/`.
  Dated live evidence for the smoke matrix and pi, Claude Code and Codex
  HARBOR-01 runs through the router is in
  `docs/evidence/api-compat/2026-10-10/` (#94).

### Changed

- `make local-router` keeps only the `big-coder` group, weighted 70% OpenAI
  `gpt-6-luna` and 30% Fireworks `accounts/fireworks/models/deepseek-v4p1-flash`.

### Fixed

- The response cache key includes the choice count `n`, so an `n=2` request is
  no longer served a cached `n=1` response.
- Upstream network errors no longer include the provider URL in
  `error.details.last_error`.
- Tool-less same-dialect Anthropic Messages and Responses requests are
  forwarded verbatim through the native passthrough codec instead of the
  re-encoder. Anthropic keeps `thinking`, `output_config`, system blocks,
  document and unknown blocks and the upstream `msg_` id. Responses keeps the
  upstream `resp_` id (so `previous_response_id` continuation works), reasoning
  items, item-array input and `incomplete` status. Under `requested_group`
  only the top-level `model` is replaced (#94).
- Images nested in Anthropic `tool_result` content now trigger image
  eligibility, so a text-only target no longer receives them (#94).
- A committed stream that ends without provider usage marks its settled usage
  as `usage-estimated`.
- Under `identifiers.mode: rewrite`, replayed Responses input item ids
  (`reasoning`, `message`, `function_call`) are decoded on ingress. Codex
  continuations no longer fail with `string_above_max_length` on
  `input[N].id`.

## [4.0.2] - 2026-10-01

### Fixes

- Replace `github.com/secure-io/siv-go` with an in-tree pure-Go AES-SIV-CMAC
  (`internal/aessiv`) for `identifiers.mode: rewrite`. The abandoned amd64
  assembly path SIGSEGV'd under concurrent SSE tool-id rewrite; wire format
  stays RFC 5297 / existing `mr_` tokens. Serialize Seal/Open on the shared
  AEAD and add concurrent round-trip coverage (#246).

## [4.0.1] - 2026-10-01

### Fixes

- Restore rewritten `mr_` identifiers on OpenAI Chat `tool_calls[].id` and
  Anthropic `tool_use` content-block `id` during ingress decode so tool
  continuations round-trip under `identifiers.mode: rewrite` (#244).

### Features

- Add optional DB-backed router config source of truth
  (`server.config_source.mode: database`) with import/export/activate/rollback,
  relational projection, and caller issue/rotate/revoke against the active set
  (#242, #243). File-mode installs are unchanged.

### Documentation

- Add an OpenJev task-class external routing example: policy service, Shadeform
  runbook, synthetic harness, and live evidence
  (`examples/external-routing-policy/openjev_*`, `docs/OPENJEV_ROUTING_DEMO.md`,
  `docs/evidence/openjev-routing/`) (#241).
- Add `make local-router` for a weighted local coding-agent instance from
  source (`scripts/local_pi_instance.py`).

## [4.0.0] - 2026-09-21

### Breaking Changes

- Removed the Fleet lifecycle subsystem: `metrum-ai-router-fleetctl`,
  `metrum-ai-router-fleet-sign`, rename stubs, `internal/fleet`, Fleet-only
  scripts, and package packaging of those binaries. Self-managed installs use
  binary, Docker Compose, or Kubernetes manifests with `metrum-ai-routerctl`.
- Config key `server.license` is rejected at startup. Remove the block before
  upgrade. Existing `license.json` files stay unread and are not a deployment
  prerequisite.

## [3.0.0] - 2026-09-19

### Breaking Changes

- Runtime licensing enforcement is removed. Existing `license.json` files are
  inert and are never read; no migration is required.
- Config key `server.license` is accepted and ignored with one startup warning
  in 3.0.0; it will be rejected in 4.0.0.
- Removed reachable `license-*` error codes from the request path.
- Removed Prometheus series: `metrum_ai_router_license_valid`,
  `metrum_ai_router_license_seconds_until_expiry`,
  `metrum_ai_router_license_grace_active`,
  `metrum_ai_router_license_validation_failures_total`.
- Removed license fields from `/version`, readiness/admin metadata, and the
  diagnostics schema (`license_compile_mode` and related `license_*` columns
  are no longer populated).
- Removed packaged binaries `metrum-ai-router-license` and
  `metrum-ai-router-customer-lifecycle`, plus source-only rename stubs
  `router-license`, `metrum-genai-smartrouter-license`, and
  `metrum-genai-customer-lifecycle`.
- Removed `metrum-ai-routerctl license` and `metrum-ai-router-fleetctl licenses`
  inventory commands. Fleet plan/deploy/status and fleet-sign deployment-intent
  signing remain.
- No usage-database migration in this release (package-only rollback).

### Features

- Record Learned Routing Policy sidecar phase latency (receive, tokenize,
  embed, featurize, predict, select, respond) as bounded histograms so
  operators can attribute sidecar time without logging prompt text (#216).

### Documentation

- Add vLLM Semantic Router and NVIDIA Switchyard to the competitive
  comparison (#212).
- Propose catalog ownership for private targets versus the signed bundle
  (#213).
- Remove deployment host identifiers from public LRP evidence and tighten
  public-face checks (#214).
- Publish the Harbor phase-latency measurement protocol. Measured Harbor
  timings are not included in this release (#216).

### Notes

- Existing `license.json` files and legacy `server.license` config remain inert
  (ignored with one startup warning through 3.0.0).

## [2.2.0] - 2026-09-19

### Features

- Add AES-SIV identifier transform with field-level native SSE ID rewriting so
  upstream identifiers are not exposed on the wire (#205).
- Add `internal/stream` translators and SSE fixture harness; ship incremental
  native Responses streaming (P1, #206).
- Translate Responses caller ↔ Chat upstream SSE incrementally (P2, #207).
- Translate Chat caller ↔ Responses upstream SSE incrementally (P3, #208).
- Translate Anthropic ↔ OpenAI Chat/Responses text and tool SSE incrementally
  (P4, #209); reasoning stays on native Anthropic routes.

### Fixes

- Pass through identifiers in api-compat mock offline configs and LRP e2e
  harness config after identifier rewrite became required.
- Drop AppArmor profile loading from LRP synthetic CI so privileged Docker
  self-hosted runners can verify bubblewrap isolation (#210).

### Documentation

- Update API compatibility and architecture-limitation docs for incremental
  bridge streaming and the `server.streaming.translator` settings.

### Notes

- Default `server.streaming.translator` remains `incremental` on implemented
  paths; `synthesized` retains unary upstream plus synthesized caller SSE.
- No usage-database migration in this release (package-only rollback).

## [2.1.0] - 2026-09-14

### Features

- Persist optional cached-input token counts and
  `cached_input_price_per_million_usd` in usage evidence (#160 / #166), with
  usage migration `2026091301` (schema version 5; `restore-required` for
  package rollback).
- Add offline LRP seed-corpus tooling for iteration 1 (#157 / #163), including
  download, extract, stratified sample, targets, replay spend guards, and
  verifier label helpers.
- Pivot the iteration-1 seed portfolio off OpenAI to OpenRouter Qwen + MiniMax,
  Fireworks kimi-k2p7-code, and Baseten GLM-5.2 (#179).
- Add offline LRP routing-benchmark harness (#159 / #165) and land CPU A/B/C plus
  GPU enforce evidence scalars (#174, #176, #181).
- Add pluggable LRP embedder backends (`onnxruntime`, `sentence-transformers`)
  and explicit device selection with `--strict-device` (#156 A/C / #180).

### Fixes

- Close LRP serving, eval parity, drift, and signed-bundle gaps (#158 / #164).
- Fix EKS NetworkPolicy tests after the `metrum-ai-router` rename.

### Documentation

- Document GPU-required LRP evidence training policy (#170).
- Cap the example seed corpus at 100 traces and raise the seed spend abort to
  100 USD (#171, #172).
- Publish public seed-example and routing-benchmark evidence scalars (#173,
  #176, #181).
- Restructure buyer-facing README / solution brief around Learned Routing Policy
  (#128, #155).

### Notes

- LRP remains opt-in (`strategy: external`). Installing this release does not
  start a policy service or authorize live enforcement.
- Report absolute USD and token categories for LRP evidence; do not invent
  savings percentages.

## [2.0.0] - 2026-09-12

### Breaking Changes

- Packaged CLIs and archives use the canonical slug `metrum-ai-router` only.
  Rename stub binaries are **not** packaged (source-only exit-2 notices under
  `cmd/` remain for local builds).
- Binary mapping:
  - `metrum-router` → `metrum-ai-router`
  - `metrum-router-token-gen` → `metrum-ai-router-token-gen`
  - `metrum-router-usage-report` → `metrum-ai-router-usage-report`
  - `metrum-router-migrate` → `metrum-ai-router-migrate`
  - `metrum-routerctl` → `metrum-ai-routerctl`
  - `metrum-genai-smartrouter-fleetctl` → `metrum-ai-router-fleetctl`
  - `metrum-genai-smartrouter-fleet-sign` → `metrum-ai-router-fleet-sign`
  - `metrum-genai-smartrouter-license` → `metrum-ai-router-license`
  - `metrum-genai-customer-lifecycle` → `metrum-ai-router-customer-lifecycle`
- Release archives and Docker image tags use `metrum-ai-router-*` /
  `metrum-ai-router:<tag>` (not `metrum-router-*`).
- Client examples that set Codex/Claude `model_provider` should use
  `metrum-ai-router` instead of `metrum-router`.
- Removed from packages and the runtime image: `router`, `router-token-gen`,
  `router-usage-report`, `router-migrate`, `smartrouterctl`,
  `metrum-genai-smartrouterctl`, `metrum-fleetctl`, `metrum-smartrouterctl`,
  `metrum-fleet-sign`, and `router-license`.

### Documentation

- Added this Keep a Changelog file for the v2.0.0 breaking release.
- Updated package bootstrap docs, installation guides, release notes, upgrade
  guide, and the production EKS deploy prompt for the new binary and artifact
  names.
- Public docs URL remains `https://llm-api.apps.metrum.ai/docs` until
  docs.metrum.ai hosting is available.

### Notes

- Go module path remains `github.com/metrum-ai/router`.
- Compose environment variable rename (`SMART_LLMROUTER_VERSION` →
  `METRUM_AI_ROUTER_VERSION`) and related runtime/metrics/k8s identity updates
  are coordinated in the runtime/deploy rename PR.

[Unreleased]: https://github.com/metrum-ai/router/compare/v4.0.1...HEAD
[4.0.1]: https://github.com/metrum-ai/router/compare/v4.0.0...v4.0.1
[4.0.0]: https://github.com/metrum-ai/router/compare/v3.0.0...v4.0.0
[3.0.0]: https://github.com/metrum-ai/router/compare/v2.2.0...v3.0.0
[2.2.0]: https://github.com/metrum-ai/router/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/metrum-ai/router/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/metrum-ai/router/compare/v1.4.4...v2.0.0
