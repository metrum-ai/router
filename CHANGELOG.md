# Changelog

All notable changes to Metrum AI Router are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [4.5.0] - 2026-11-04

Proof release: each claim below ships with the run, the configuration and the
limits that produced it, and each proof ships the command that reruns it.
Negative and partial results are published as such, not dropped.

### Proof

- Router overhead benchmark (#422): a paired load driver, a benchmark-only
  profiling wrapper and an explicit profile occupancy floor measure added
  p50/p99 latency and streaming first-content latency against a warmed vLLM
  backend, direct versus routed. Published evidence covers the plain router
  path and real d3 shadow and enforce paths on A100 hosts, including the
  high-concurrency decision-path deltas and their limits
  (`benchmarks/overhead/evidence/`). CPU and memory per 1,000 requests/s,
  maximum sustained throughput and the edge case are still missing, so #422
  stays open. Rerun: `make overhead-profile-smoke`,
  `make overhead-decision-smoke`.

- Savings case studies on three real workloads (#423): coding agent, support
  agent and document Q&A, each routed against an all-frontier baseline on the
  same tasks with a scored quality arm, paired trials, Wilson intervals and
  per-request cost (`examples/case-studies/`,
  `docs-site/docs/evaluation/savings-case-studies.md`). Provider charges for
  the OpenAI requests are reconciled against provider usage in both arms; the
  Fireworks side of the routed arms is a list-rate estimate, labelled as one.
  The coding-agent study lost quality and is published that way.

- Examples catalog (#424): tokenomics, governance, escalation continuum, domain
  experts, decision models and KV-cache cost, each a runnable demo with mock
  and self-hosted or hosted paths, sanitized evidence and stated limits
  (`examples/`). Mock results are labelled wiring checks.

- Expert routing demo (#426): a four-domain expert group against a
  tensor-parallel general model on the same GPU count, with the negative result
  published — the general model passed more strict checks than the routed
  experts on the fixed 88-case cohort, and classifier accuracy is reported
  separately from route accuracy (`docs/evidence/expert-routing/`).

- Escalation continuum (#427): local tier → datacenter GPU → frontier API on
  synthetic labeled cases, run on AMD MI300X and NVIDIA A6000, including the
  illustrative latency target each run missed
  (`examples/escalation-continuum/evidence/`,
  `docs-site/docs/evaluation/escalation-and-decision-evidence.md`). Local and
  GPU costs exclude hardware and energy, and provider figures are estimates
  with the invoice pending.

- Decision-model routing (#428): d3 and Clef adapters in shadow and enforce, a
  labeled routing probe with two-tier confusion matrices and policy-duration
  percentiles, and a bounded real d3/Clef observation on an A6000
  (`examples/decision-models/evidence/`). OpenJev is excluded from the v4.5
  comparison for licensing and edge results are missing; both are stated, and
  no co-located added-latency claim is made.

- Agent tool-call and router-chain compatibility (#429, #430): real agent
  clients (pi, LangGraph, OpenAI Agents SDK, Claude Code and Codex) exercising
  streamed tool calls and continuations directly and through router chains of
  depth 1, 2, 3 and 5, with a scheduled matrix that publishes per-cell reports
  (`docs-site/docs/evaluation/tool-call-proof.md`,
  `docs-site/docs/reference/api-compatibility.md`, `tests/agent_compat/`).
  These are deterministic mock cells, not live-provider certification.

- Governance under load (#431): reservation, rate and concurrency, PII
  redaction and security-export probes under concurrency, with exact admission,
  rejection and settlement counts and per-key usage reconciliation
  (`docs/GOVERNANCE_LOAD.md`, `docs/GOVERNANCE_HTTP_LOAD.md`,
  `docs/GOVERNANCE_PII_LOAD.md`, `docs/GOVERNANCE_AUDIT_LOAD.md`).
  Rerun: `make governance-load`.

- Fine-tune example: the vulnerable Axolotl launcher, its historical inventory
  lock and its schema-install CI are retired in v4.5.0 and the example says so,
  keeping the configurations and recorded results as historical records. Fine-
  tune run 3 is published as a gate failure with its transport defect, and the
  replacement trainer is deferred to v4.6.0 (#509).

### API compatibility

- OpenAI and Anthropic compatibility runs on every pull request and again on
  the merge group: the pinned official OpenAI and Anthropic testers run inside
  the required `ci` check, and the Go and TypeScript SDK suites run in the
  supplemental checks, all against loopback mocks with issue-linked expected
  failures recorded against main (#535, #446, #549).

- Chat streaming exposes a usage-only chunk only when the caller asks for it
  with `stream_options.include_usage` (#528).

- `GET /v1/models/{id}` serves caller-filtered model lookup, including IDs that
  contain slashes; unknown and unauthorized IDs both return an indistinguishable
  `model_not_found` 404 (#529).

- Errors follow the caller's dialect and disclose nothing about models the
  caller cannot use: an unknown model and a disallowed one return the same
  `404 model_not_found` (OpenAI) or `404 not_found_error` (Anthropic) before any
  upstream call (#530).

- A capability or dialect that no configured target can serve returns `400`
  with `code: unsupported_capability` and `param` naming the field instead of
  `502 no-eligible-target` (#530), and a terminal provider request error keeps
  its status (`400`, `404`, `413`, `422`) as `code: upstream-rejected` without
  the provider body instead of becoming `502 upstream-failed` (#530).

- Provider credential, balance and entitlement failures (`401`, `402`, `403`)
  return `503 upstream-access-denied` rather than a caller authentication error
  (#530).

- Endpoints the router does not serve answer with an authenticated, dialect-
  shaped `501 unsupported_endpoint`: OpenAI legacy completions, moderations,
  embeddings, stored Responses, files, batches, images, audio, vector stores,
  assistants, threads, fine-tuning, realtime, videos, containers, conversations,
  agents, ChatKit threads and skills, and Anthropic legacy `/v1/complete`,
  message batches and beta files (#531, #580). Every one of those paths answers
  in the caller's own dialect rather than leaving late-added OpenAI surfaces on a
  bare `404`/`405` or answering `/v1/skills` in the Anthropic envelope.

- Anthropic clients get a caller-filtered model catalog and model lookup in
  Anthropic format (#538), and document input is a declared, routable modality
  for Anthropic Messages and OpenAI content parts (#537).

- Cross-dialect structured output and tool translation are validated against
  real models, and a translated tool request is eligible only when the target
  declares the caller dialect, `validation_status: passed` and the matching tool
  capability on both sides (#539).

- The generated starter configuration covers Responses, tools, structured
  output and vision, with a pinned starter smoke (#534).
- Responses `input_tokens` counts input through a native Responses target
  without billing tokens, and `compact` performs stateless compaction with the
  same token admission and metering as Responses while bypassing the response
  cache (#531). Both require a native Responses target in the requested group.

### CI

- Required CI fans out into three lanes: `router` across six measured shards,
  `other-go` for the rest of the Go suite and the Windows cross-builds, and
  `other` for the pinned SDK compatibility testers against loopback mocks, the
  Python checks and the optional example suites. One aggregate `ci` job fails
  unless every lane succeeds, and `make ci` runs the same graph locally
  (#549, #569).

- The merge-queue gate measures and guards `internal/router` Go test wall time,
  warning at 6 minutes and failing at 8 (#418).

- Offline evaluation infrastructure: a preregistered matrix, a pairwise
  coverage generator with explicit exclusions, and an evaluation report schema
  validator, all enforced in CI (#447).

- A TypeScript SDK API-compat suite runs on the gate (#446), and TypeScript
  compatibility jobs provision Go modules explicitly (#506).

### Security

- GitHub build-provenance attestations for every published release archive,
  `SHA256SUMS`, and `release-artifacts.json` (#332). Windows archives are
  unsigned by default; Authenticode remains an optional signing path.

- Release workflow: Windows binaries are Authenticode-signed with Azure
  Trusted Signing (RFC 3161 timestamped) when the signing secrets are
  provisioned — the step is a logged no-op until then, so unsigned builds
  keep working. Signing runs between the Windows `.exe` builds and the
  deterministic zip packaging on tag builds only, so checksums and the
  release inventory always hash the signed binaries; a sign or verify
  failure fails the release once credentials exist (#332).

- Tenant provisioner RBAC: the Role no longer grants NetworkPolicy write
  access (trivy KSV-0056). The namespace default-deny policy moved from
  `tenant-bootstrap-resources.example.yaml` to
  `tenant-default-network-policy.example.yaml`, applied once at namespace
  creation by the platform administrator in the same reviewed step that binds
  the provisioner RBAC. Re-apply
  `deploy/kubernetes/bootstrap/tenant-provisioner-rbac.yaml` and apply the new
  template when onboarding the next tenant (#414).

- Audit-tier semgrep findings in the Admin API code are triaged, and the
  config-set YAML export sends `X-Content-Type-Options: nosniff` (#416).

- Updated `github.com/go-jose/go-jose/v4` to 4.1.6 for
  GHSA-mgcc-cww5-fp2q, a denial-of-service risk when parsing crafted RSA JWKs.
  The dependency now rejects RSA JWK moduli over 4096 bits; OIDC deployments
  using larger keys must rotate to supported keys before upgrading (#451).

- Lockfile refreshes close the reported advisories in the agent-compatibility
  project (undici), the evaluation tooling (node-forge, with a DigestInfo
  validation test) and the fine-tune evaluation tooling (nested MSAL), and the
  docs-site dependencies are updated to their patched versions (#511, #512,
  #541, #564). Remaining medium and low lockfile advisories move to v4.6.0
  (#510).

### Behavior changes

- Explicit `store:false` is now honored on native OpenAI Responses passthrough;
  it was previously dropped, allowing OpenAI's default of storing to apply
  (#624). Native caller `store:true` is also forwarded unless the target forces
  false. Translated routes do not provide stored Responses retrieval. Provider
  retention policies and router content capture are separate from this field.

- Chat audio output now requires `output_modalities: [text, audio]` on the target
  (or `[audio]` for an audio-only declaration). Native Chat targets without an
  audio declaration default to text-only; an audio request that would otherwise
  route to a text-only target returns `400 unsupported_capability` with
  `param: modalities`. Other capability or endpoint errors may take precedence.
  Audio streaming requires incremental translation; synthesized Chat streaming
  rejects audio before dispatch. Translated bridges do not support audio output
  (#612, #576).

- Chat streaming no longer sends a usage-only chunk unless the caller sets
  `stream_options.include_usage` to true (#528). Upstream usage collection for
  accounting stays enabled, and the OpenAI-compatible bridges keep forcing
  `stream_options.include_usage: true` on their own upstream requests. Clients
  that need usage in the stream must opt in; clients that tolerated a trailing
  `choices: []` chunk see one fewer chunk.

- An unknown model and a model group the caller's token may not use now return
  the same `404 model_not_found` (OpenAI) or `404 not_found_error` (Anthropic)
  before any upstream attempt, so a caller cannot probe which groups exist. A
  disallowed group no longer returns `403 model-not-allowed` (#529, #530).

- A request that no configured target can serve returns `400` with
  `code: unsupported_capability` and the offending `param` instead of a routing
  failure or an upstream round trip that strips the field (#530).

- A terminal provider request error (`400`, `404`, `413`, `422`) is preserved
  with that status and `code: upstream-rejected` instead of becoming
  `502 upstream-failed` (#530).

- Provider `401`, `402` and `403` failures return `503 upstream-access-denied`.
  Automation that treated those statuses as its own authentication failure must
  stop doing so (#530).

- Endpoints the router does not serve return `501 unsupported_endpoint` with an
  OpenAI-shaped body (`message`, `type`, `param`, `code`) for OpenAI callers and
  the Anthropic-shaped equivalent for Anthropic callers, instead of a bare
  `405`. Scripts that matched `405` for those paths must accept `501` (#531).

- YAML-configured providers can opt in to `upstream_kind: router` for a
  separately authenticated trusted router, preserving its rate-limit 429 or
  typed 503 and valid `Retry-After` after fallbacks are exhausted (#496).
  This does not trust `Via`; database-backed config-set projection rejects the
  field until it can persist the setting safely.

- Router chains append a sanitized router-only `Via` and stop at the configured
  hop limit (default 16) with HTTP 508; malformed entries are ignored, other
  proxy entries are not forwarded, and upstream 508 is not retried (#430).
  Intermediate proxies must preserve `Via` for protection.

- Native stream traces now distinguish the first observed upstream byte from
  the first flushed downstream frame (#455). Profile-paced upstream streams
  omit misleading independent upstream duration and throughput values; their
  paced proxy wall time remains available in the trace.

- Experiment profiles for endpoint-bound agent studies (#379): opt-in,
  scope-required (callers/groups/targets) profiles inject an added TTFT
  delay with seeded reproducible jitter and pace delivered output by an
  estimated token rate (`output_tokens_per_sec`, calibrated via
  `token_estimate_chars_per_token`) or an explicitly labeled event rate
  (`events_per_sec`). Pacing only delays delivery, preserves streaming
  frames byte-for-byte (tool-call, reasoning, usage and `[DONE]` events
  included), honors cancellation and timeouts, and caps the total injected
  delay per request. Every paced request records trace events with the
  profile name and profile hash; upstream timing stays measured separately
  from downstream pacing. See `docs-site/docs/configuration/experiment-profiles.md`.

- Database-backed configuration changes are announced to PostgreSQL replicas
  through LISTEN/NOTIFY, and the revision poller remains the documented
  fallback (#443, #449, #452).
- Responses `input_tokens` and `compact` are served in v4.5.0; a request that
  carries `previous_response_id` still returns `501 unsupported_endpoint`
  because stored-response affinity is not available yet (#531). Input counting
  is unbilled and the audit record carries `endpoint=responses.input_tokens`.

- The usage database moves to schema 11 (`2026103001`), which adds
  `request_usage.endpoint` so usage rows record the surface each request used,
  on SQLite and PostgreSQL. Apply it through the Required Migration Gate before
  serving: with the default `deployment-job` policy the serving process only
  validates the ledger, and `auto-safe` applies it at startup solely on a
  reviewed single-node SQLite deployment. Existing rows cannot be classified
  retroactively and keep an empty value. Downgrading to 4.4 requires restoring
  a pre-upgrade database backup (#608).

### Moved to v4.6.0

- Fine-tune run 3 and the maintained TRL + PEFT trainer (#425), the d3
  follow-up measurements (#287), embeddings (#532), stored Responses (#533) and
  the external-rerun gate line move to v4.6.0, together with the remaining
  medium and low lockfile advisories (#510). In v4.5.0 embeddings and stored
  Responses keep the documented OpenAI-style `501 unsupported_endpoint` error.

## [4.4.0] - 2026-10-27

### Added

- Admin Config API: swap, add, remove and roll back the models behind a
  model group with one authenticated call
  (`PUT /admin/config/groups/{group}/targets`), with dry-run validation,
  `If-Match` optimistic concurrency, `Idempotency-Key` replay safety,
  per-target eligibility reports, a full audit trail and
  `config export/diff/rollback` parity. Requires database-backed
  configuration mode; file mode keeps serving and answers writes with
  `409 config_source_mode_file` (#406; #395).
- Group shadow runs: `POST /admin/config/groups/{group}/shadow` shadows a
  candidate target against a group's live traffic with a sample rate and
  bounded run (requests, duration or cost cap), `GET …/shadow` reads the
  cost/token/latency/error comparison from existing usage telemetry,
  `POST …/shadow/promote` promotes in one call through the copy-on-write path
  (prior targets stay as fallback; `shadow_promote` audit events link the run),
  and `DELETE …/shadow` stops while keeping the run readable. Only aggregate
  counters are recorded — no request content or caller identity (#407; #395).
- `metrum-ai-routerctl groups` CLI (`list`, `show`, `set-targets`, `add-target`,
  `remove-target`, `rollback`, `history`, `shadow start|status|promote|stop`)
  over the Admin API with `--api` mode for `config import|export|activate|`
  `rollback|diff`; admin credentials come from environment or key files,
  never command-line flags, and mutations send generated idempotency keys.
  Docs include the Admin Config API reference and the "Switch models when
  prices change" guide (#408; #395).
- `e2e/admin-api/`: a repeatable live-upstream validation suite for the Admin
  Config API — one command builds the router, stands up a 3-replica DB-backed
  deployment plus a file-mode router, and verifies swap/shadow/promote/rollback
  against real providers with a per-case evidence report (#409; #395).
- Dedicated admin API key for `/admin/*` routes: `server.admin_auth.api_keys`,
  hashed at rest with PBKDF2-HMAC-SHA3-512, rotatable without downtime, with
  fine-grained permissions; caller and provider keys are rejected on admin
  routes. Generate keys with `metrum-ai-routerctl adminkeys generate`
  (#396; #350).

### Security

- Hardened release workflow input handling: dispatch inputs reach shell steps
  only through validated environment variables, the release tag is checked
  against a strict semver allowlist before use, and release token permissions
  are scoped to the single job that needs them (#388).
- Tightened admin authorization scope matching: policy objects now compare by
  exact equality (the only wildcard is the standalone `*`), actions are
  anchored patterns, and partial-wildcard objects are rejected at policy load
  time. A grant for one admin object no longer covers a different admin
  object; deployments whose policies relied on that must list each object
  explicitly (#389).
- Least-privilege tenant provisioner RBAC: the namespace-scoped bootstrap Role
  grants write access only to the resources the tenant templates create
  (ResourceQuota, LimitRange, NetworkPolicy, namespace-scoped Linkerd
  Server/ServerAuthorization), Ingress is read-only, and no delete verbs or
  wildcards remain. Re-apply the manifests to upgrade existing tenants (#391;
  #365).
- Dependency updates for patched advisories: body-parser 1.20.9,
  serialize-javascript 7.1.4, katex 0.18.10, postcss-selector-parser 7.1.7
  (docs site); filelock 4.2.4, setuptools 84.2.1 (learned routing policy);
  pytest 9.0.3 (api_compat and harbor test projects); datasets 5.0.1 with an
  fsspec floor (LiveCodeBench evaluator contract) (#394, #398; #364).
- Scanner policy refresh: withdrawn and stale grype exceptions were removed
  and the single still-applying build-tool exception renewed with an owner and
  an upstream-tied expiry (#390).
- Bedrock bridge token limits are range-checked before conversion, so
  out-of-range `max_tokens`/`max_completion_tokens` request values clamp
  instead of wrapping (#392).

### Changed

- CI: temporary router-only test timeout headroom on shared-CPU runner nodes
  (#377); Dependabot version updates observe a cooldown and now cover the
  evaluator and test pip projects (#393); documentation clarifies building
  router Docker images (#378).

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
  (`examples/external-routing-policy/openjev_*`, `docs/OPENJEV_ROUTING_DEMO.md`)
  (#241). OpenJev is excluded from the v4.5 comparison for licensing.
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

[Unreleased]: https://github.com/metrum-ai/router/compare/v4.5.0...HEAD
[4.5.0]: https://github.com/metrum-ai/router/compare/v4.4.0...v4.5.0
[4.2.0]: https://github.com/metrum-ai/router/compare/v4.1.0...v4.2.0
[4.1.0]: https://github.com/metrum-ai/router/compare/v4.0.2...v4.1.0
[4.0.2]: https://github.com/metrum-ai/router/compare/v4.0.1...v4.0.2
[4.0.1]: https://github.com/metrum-ai/router/compare/v4.0.0...v4.0.1
[4.0.0]: https://github.com/metrum-ai/router/compare/v3.0.0...v4.0.0
[3.0.0]: https://github.com/metrum-ai/router/compare/v2.2.0...v3.0.0
[2.2.0]: https://github.com/metrum-ai/router/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/metrum-ai/router/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/metrum-ai/router/compare/v1.4.4...v2.0.0
