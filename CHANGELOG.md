# Changelog

All notable changes to Metrum AI Router are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [4.3.0] - 2026-10-24

### Added

- Governed trace export to canonical training/replay JSONL: opt-in encrypted
  capture, outcome labels (`POST /admin/training/outcomes/{request_id}`),
  `POST /admin/training/export`, filters, redaction/PII checks, explicit admin
  authorization and durable audit, deduplication, provenance manifests and
  frozen time splits. Heldout failures retain their full cohort; future exports
  collapse identical tool declarations and reject conflicts with manifest
  counts. Existing captures/exports are not retroactively rewritten
  (#361, #368, #373; #358).
- AWS Bedrock Converse/ConverseStream upstream for validated open-weight model
  families, with IAM/default-chain credentials, region/inference-profile IDs,
  tool/SSE bridging, usage accounting and throttle failover (#349).
- Customer-owned Qwen BF16 LoRA fine-tuning/private-serving example: governed
  router-centric capture/train/evaluate loop, deterministic tool harness,
  key-scoped same-group canary, frozen gates, NVIDIA and AMD evidence and
  teardown receipts. Run 1, run 2 and AMD all **failed** the preregistered gate
  and were not promoted; the router kept the teacher in place. These results
  establish neither API parity nor quality-equivalent savings. Teacher-output
  rights are scoped to the synthetic example, not customer-data permission
  (#354, #360, #369).

### Changed

- Empty legacy `modalities: []` is treated as unset and omitted on config
  serialization; explicit nonempty legacy capabilities still override input
  metadata. Disable a target explicitly instead of using an empty list (#372).
- Native Anthropic `count_tokens` proxies upstream counts/errors without
  inference billing or quota. Other transports return marked estimates; count
  events remain visible separately from inference reports (#353).
- Usage provenance distinguishes reported from estimated tokens; missing stream
  usage settles from observed text, not the requested output ceiling. Native
  Chat requests final usage unless explicitly configured otherwise; historical
  provenance remains unknown (#348).
- CI uses ephemeral `router-arc` ARC pods with Actions-backed caches. Independent
  deterministic lanes retain the strict aggregate gate. Earlier persistent-host
  cache tuning is superseded for active workflows; legacy weekly host cache
  maintenance remains separate. No end-to-end CI performance parity is claimed
  (#341, #342, #375).
- Documentation consolidates Bedrock, accounting, migrations, governed training,
  measured evidence and ARC operations (#356).

### Fixed

- Gemini thinking tokens contribute to output/reasoning usage and output cost;
  tool-use prompt/image details are retained, and text-only Chat streaming can
  be synthesized from unary generateContent (#351).
- Anthropic input consistently includes uncached, cache-read and cache-write
  tokens without double-counting. Optional cache-write pricing falls back to
  base input pricing with a warning when absent (#352).
- Usage parsing preserves explicit zero counts and alternate numeric decoders,
  aligns reasoning with served output, retains stream totals and tolerates
  private Anthropic thinking blocks (#347).
- Reasoning/cache subsets, known reporting coverage and disjoint cost/savings
  components survive rollups, reports, CLI/admin surfaces and `/metrics`.
  Retained history accepts only complete closed UTC days finalized after the
  window ended; unknown historical coverage is not fabricated (#357, #362).
- Isolated SQLite test fixtures clone real initialized schemas without sharing
  mutable state or weakening migration/startup assertions (#363).
- Example harness waits for router readiness and requires authenticated route,
  target and grader preflight before batches. Local trace/evaluation callers
  have high limits; unexpected pre-route throttling aborts the whole run, with
  tiny limiter tests explicitly isolated (#360, #369, #370, #374).

### Security

- Example localhost NVIDIA/AMD proxies validate allowlisted response headers
  and reject CRLF/header injection (#360).

## [4.2.0] - 2026-10-21

### Fixed

- Pi smoke explicitly sequences write/result/read/result to avoid model-side parallel tool races while retaining strict tool-error and router checks (#333).

- SSE translation recognizes bare CR, LF and CRLF consistently, including
  line endings split across reads; tool streams no longer retain literal CR
  field values or lose them on serialization (#331, fixes #327).
- Native same-dialect Chat preserves sampling controls, user, all choices,
  refusals and logprobs, including text-only requests. Multiple-choice,
  refusal, tool and logprob responses bypass the normalized text cache.
  Present Chat `max_tokens` must be an integer >= 1 or returns HTTP 400
  before upstream, on text and tools paths (#321).
- Streams disable proxy buffering, reject non-SSE replies with
  `502 upstream_protocol_error`, and cap SSE lines at 64 KiB
  (`upstream_frame_too_large`). Post-commit errors terminate the stream (#323).
- Local pi setup preserves manual edits and routerctl callers; configure-pi
  no longer regenerates config. Explicit regeneration uses collision-proof
  backups; fresh configs enable scoped security reports (#316).
- Pi smoke reports escaped proof comparisons and serving upstreams without
  hiding substantive failures behind retries (#317); local-router test
  instructions clarified (#277).
- Shadow external-policy telemetry records catalog-backed recommendations,
  including candidate zero, separately from the baseline served (#314).
- Recognized revoked keys retain safe caller/public-token attribution in scoped
  security and usage reports; unknown tokens remain unattributed (#319).
- Compose validator, upgrade and cutover use `METRUM_AI_ROUTER_VERSION` (#294).
- Local router initial builds, supervisor rebuilds, and generated start scripts
  now embed the same version, commit, and UTC build date as release builds (#307).

### Added

- Windows amd64 and arm64 binary ZIP packages alongside Linux tarballs (#291).
- Targets can declare `modalities` and `structured_output`. Unsupported media
  and translated unknown Anthropic blocks return explicit 400s instead of
  losing content. Supported documents/images survive streaming and tool
  bridges; Anthropic JSON-schema formats translate to OpenAI. Anthropic server
  search and Responses hosted tools consistently reject before upstream rather
  than silently stripping descriptors (W2b #329).
- Claude Code client-tool calls and tool-result continuations on explicitly
  validated OpenAI Chat and Responses targets, including streaming and identifier
  rewriting. Distinct Anthropic tool evidence is required; native routes are
  unchanged (#315).
- Asynchronous usage writer for SQLite and PostgreSQL: bounded 10,000-entry
  queue, 256-entry/250 ms batches, configurable graceful drain, queue/drop/write
  latency metrics and a PostgreSQL profile. Saturation drops telemetry rather
  than delaying inference; reports and encrypted captures are eventually
  consistent (#325).
- Nightly SSE, canonical Chat IR and config-loader fuzz checks with seed corpus
  and crash artifacts (#324).
- Decision 3.0 (d3) external routing policy reference (#278):
  `examples/external-routing-policy/d3_policy.py` asks a vLLM Semantic Router
  d3 System One server typed questions about the redacted request text and
  maps the answers to `strong` or `cheap` eligible targets with declarative
  rules. d3 failures return HTTP 502 so the group's `on_error` decides. Adds a
  scripted fake d3, unit tests (`make d3-policy-test`, in `make test-fast`), a
  credential-free router e2e (`make d3-policy-e2e`, in `make test`), the
  `d3-policy-demo` reference group, a docs page, and an optional `d3-auto`
  group for `scripts/local_pi_instance.py` (`--d3-policy-url`).

### Security

- Routerctl raw-config parse errors redact literal credentials and truncated
  YAML quotes; atomic-write docs clarify non-expanding validation (#313).
- Grype exceptions added 2026-10-21 are narrowly pinned to TypeScript native
  `tsc` build dependencies; compiler binaries are not shipped (#318).
- The outcome-calibrated policy example now parses `[[outcome-calibration:…]]`
  and `[[outcome-live-demo:…]]` markers with a linear scan instead of
  backtracking regexes, so crafted request text cannot stall it (CodeQL
  `py/polynomial-redos`). Valid markers parse as before.
- `scripts/validate_production_bundle_config.py` now requires
  `--require-trusted-proxy` to be a CIDR and prints only its canonical form.
  `scripts/check_env_example_secrets.py` reports key names and paths, never
  values. This clears CodeQL `py/clear-text-logging-sensitive-data`.
- Config load and router startup errors no longer print secret values.
  `LoadConfig`, `LoadActiveCanonicalConfig`, `LoadConfigForServe` and `New`
  replace every expanded `${NAME}` or env.json value, every value resolved
  from a `*_env` field (`password_env`, `api_key_env`, `dsn_env`, and so on),
  literal credential fields and credential headers with a label such as
  `[redacted: value of $NAME]`. Errors still name the field and the env var.
  This also covers the 7-character prefix that yaml.v3 quotes in type errors,
  DSNs that pgx echoes back when it cannot parse them, and a credential pasted
  into a `*_env` field instead of a variable name (CodeQL
  `go/clear-text-logging`, alerts #6 and #7).
- `metrum-ai-routerctl providers list` and `providers upsert` print
  `api_key_env` only when it is a valid environment variable name, and remove
  `user:password@` userinfo from the `base_url` they print (CodeQL
  `go/clear-text-logging`, alert #8).
- `server.admin_reports.path_prefix` must be a plain local path. Values that
  start with `//` or `/\`, or that contain a backslash, `%`, `?`, `#`, `:` or a
  control character, now fail validation. The reports handler never redirects
  to such a prefix (CodeQL `go/bad-redirect-check`, alert #12).
- Caller bearer tokens are now verified against `callers[].token_hash`, a
  salted PBKDF2-HMAC-SHA3-512 verifier
  (`pbkdf2-sha3-512:i=<iterations>:<salt>:<key>`, pure Go `crypto/pbkdf2` and
  `crypto/sha3`), instead of an unsalted SHA-256 digest (code scanning alert
  #11, `go/weak-sensitive-data-hashing`). The router finds the candidate
  caller by its public `token_id` prefix, runs the expensive check once per
  process, and then authenticates with a constant-time HMAC-SHA3-512 digest
  under a per-process random key. A cached request costs about 4.7 µs against
  about 1.5–2.6 µs before. First-use verification is about 40 ms and runs in a
  bounded pool.
- The Redis bridge-session backend fingerprint is no longer a hash. It is a
  comparable in-process struct key holding only connection settings and
  credential environment-variable *names*, never their values or inline
  credentials (alert #10). Redis session keys move to a SHA3-512 `bridge-session-v2`
  layout. For one TTL plus one hour after startup, the router also reads,
  writes and deletes the old layout, so rolling upgrades keep in-flight
  sessions.

### Changed

- Reference default and local `big-coder` groups route eligible Chat requests
  70% to Fireworks DeepSeek Flash 1017 and 30% to OpenAI Luna; Responses use
  Luna (#306). Existing local configs are preserved, not silently regenerated.
- Explicit `eligibility.implicit_openai_to_anthropic` migration flag (default
  true now, false next minor), startup warnings and dry-run/diff
  `config patch-eligibility` command. Adaptive thinking requires reasoning
  support; target/provider-model `allowed_betas` constrain forwarded betas and
  known beta-gated body fields (#322).
- Continuations exclude `force_store_false` targets and return HTTP 400
  `continuation_unsupported` when none qualify. Chat-to-Responses is stateless
  (`store:false`, no `previous_response_id`) even with legacy sessions (#323).
- Usage schema v6 separates `cost_list_usd` and `cost_billed_usd`. Router-cache
  hits bill zero while retaining list estimates; reports expose billed spend
  and cache savings (#324).
- Docs/admin dependencies and CA bundles refreshed; CI uses setup-uv 10.3,
  upload-artifact 7.0.2 and setup-node 7.1; lucide-react 1.55 and Go crypto
  0.58 updated. LRP sandbox prerequisites are conditional and pyarrow lock
  metadata synchronized (#297, #298, #299, #301, #302, #311, #318).
- Public docs link to Metrum AI; d3 reference evidence includes GPU measurements,
  selectable price tiers, measured baselines, and qualified small-run and
  30-day savings (#279, #288, #289, #310).
- `metrum-ai-routerctl callers generate|rotate`, `metrum-ai-router-token-gen`
  and the database-mode caller admin API issue `token_hash` by default.
  `--hash-format legacy-sha256` keeps the old output for older routers. New
  `metrum-ai-routerctl callers hash` converts an existing token to
  `token_hash` without rotating it, and `callers verify` checks a token
  against a configured or given `token_hash`.
- `config.example.yaml`, `scripts/local_pi_instance.py` and the production
  bundle validator use or accept `token_hash`. The control-plane database
  stores it in the existing caller verifier column, so no schema migration is
  needed.

### Deprecated

- `callers[].token_sha256` still authenticates but logs
  `caller <id> uses deprecated token_sha256` at startup and on reload, and
  will be removed in a future major release (#281). Convert with
  `callers hash` or rotate; see the upgrade guide. A caller must not set both
  fields.

## [4.1.0] - 2026-10-10

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
- Tagged GitHub releases publish the binary packages
  (`metrum-ai-router-<version>-linux-<arch>.tar.gz`), `SHA256SUMS` and
  `release-artifacts.json` only. Docker packages are no longer attached to the
  GitHub Release; build them from a checkout with `make package-docker` (#248).
- The admin web toolchain moves to Vite 8, TypeScript 7, Vitest 5,
  Tailwind CSS 4 (CSS-first, autoprefixer removed) and React 19.3 with paired
  type packages. The embedded admin dist is rebuilt; only intended
  text-opacity rendering changes (#221, #225).
- Docs site: `@mermaid-js/layout-elk` 1.0.1 with Mermaid 12, Node `>=22.12`.
  The docs origin is set in one file (`docs-site/docs-origin.json`), and
  `make docs-qa` rejects non-canonical docs origins (#224, #17, #123).
- Learned routing policy service: pandas 3, mypy 2 and sentence-transformers 6
  are allowed, and `uv.lock` is refreshed (#223).
- CI: `actions/checkout` v7, `actions/setup-python` v7, `astral-sh/setup-uv`
  v10.2 and `anchore/sbom-action` 0.24.3 (#222).
- Go modules: `pgx/v5` 5.11.0, `golang.org/x/crypto` 0.57.0, `casbin/v2`
  2.135.0, `go-redis/v9` 9.23.0.

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
- Local-router GPT pricing entries and Node scan dependencies are corrected
  (#252).
- Under `identifiers.mode: rewrite`, replayed Responses input item ids
  (`reasoning`, `message`, `function_call`) are decoded on ingress. Codex
  continuations no longer fail with `string_above_max_length` on
  `input[N].id`.

### Security

- Build with Go 1.26.9 (`go.mod` toolchain and the Docker build image), which
  fixes standard-library vulnerabilities in the `net/http` HTTP/1 and HTTP/2
  client and server (Trailer, CONNECT, Range, flow control, framing, HPACK),
  `net/textproto` MIME header limits and `crypto/tls` ECH: GO-2026-6603,
  GO-2026-6605, GO-2026-6607, GO-2026-6608, GO-2026-6609, GO-2026-6610,
  GO-2026-6611, GO-2026-6612, GO-2026-6613 and GO-2026-6617.
- Docs-site `image-size` 2.0.4 and `http-cache-semantics` 4.3.0 clear their
  advisories, and their `.grype.yaml` exceptions are removed. `braces` stays
  excepted (no fixed release). A location-scoped exception covers the Go
  toolchain embedded in TypeScript 7's build-time `tsc` binary, which is not
  shipped in the router (#253).

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

[Unreleased]: https://github.com/metrum-ai/router/compare/v4.2.0...HEAD
[4.2.0]: https://github.com/metrum-ai/router/compare/v4.1.0...v4.2.0
[4.1.0]: https://github.com/metrum-ai/router/compare/v4.0.2...v4.1.0
[4.0.2]: https://github.com/metrum-ai/router/compare/v4.0.1...v4.0.2
[4.0.1]: https://github.com/metrum-ai/router/compare/v4.0.0...v4.0.1
[4.0.0]: https://github.com/metrum-ai/router/compare/v3.0.0...v4.0.0
[3.0.0]: https://github.com/metrum-ai/router/compare/v2.2.0...v3.0.0
[2.2.0]: https://github.com/metrum-ai/router/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/metrum-ai/router/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/metrum-ai/router/compare/v1.4.4...v2.0.0
