# Changelog

All notable changes to Metrum AI Router are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Admin machine authentication on the dedicated admin listener: configured
  admin API keys, OAuth 2.0 client-credentials and workload identity federation
  tokens with DPoP or certificate sender constraints, mTLS trust domains, and
  offline-enrolled emergency keys and certificates. Every request captures one
  immutable authentication/authorization generation and every operation still
  goes through the single authorization decision point.

### Fixed

- `metrum-ai-router-migrate --json` prints the status document while a migration
  is pending instead of an empty line with a zero exit. A pending migration is
  described by its identity, integrity and execution metadata (id, name,
  release, checksum, manifest digest, execution and maintenance mode, lock,
  timeout and rollback class, data-job key, postcondition and dependencies); its
  handler functions are never encoded. An encoding failure is a non-zero exit
  with a bounded error, and the deployment-job steps in
  `docs/DATA_MIGRATIONS.md` have a regression test that runs
  `--action=plan --json` and `--action=status --json` against a ledger with
  pending migrations (#807).

### Security

- configured admin keys with a non-lowercase disabled status were still accepted
  on the serving port.
- Serialize PostgreSQL revision-notifier shutdown with its listener goroutine
  and await pgx asynchronous connection cleanup, avoiding concurrent connection
  reads and closure during service shutdown.
- Add typed safe authorization problems and a trusted PostgreSQL policy-mutation
  freshness gate, backed by complete revision synchronization. The shared
  `/admin/v1` mount marks mutations from the trusted registry entry's declared
  method before any decision, so every non-read operation (emergency included)
  answers `503 policy-stale` on an expired clock while reads stay available;
  wrapped typed errors are mapped with `errors.As`. Failed reads or builds
  preserve the previous authorizer and successful-sync time, and the observation
  time is sampled before the shared-locked read. `admin_auth.authorization.
  max_staleness` is validated and applied at startup and on reload (never a
  request-time 422; a configured 10s window makes the policy stale at 10s+1ns),
  and the machine authorizer now has one publisher: a configuration reload asks
  the synchronized path to publish instead of storing its own authorizer. The
  policy runtime migration, audit/replay and reverse configuration guards remain
  separate control-plane integration requirements.

  Release note: with PostgreSQL authority and DB-sourced policy, `/admin/v1`
  mutations answer `503 policy-stale` until the policy runtime table exists. That
  table and its ledger migration ship in the guarded policy-resource change
  (#866) in the same release; reads and static/SQLite authority are unaffected,
  and restore recovery today is a restart (an operator reseed ships with #866).
- v4.8.0 release binaries are built with Go 1.26.10 for standard-library
  security fixes. Source builds require at least Go 1.26.10; CI, release and
  container build producers use the patched toolchain.

- Guarded policy resources on `/admin/v1`: 19 typed operations (set/rule/membership list-create-get-replace-delete, validate, diff, activate, rollback) inside one mutex plus an exclusive singleton transaction, with actor-bound authorization before any precondition work, immutable active/retired content, clone-rollback with fresh IDs and a last-normal-administrator guard. Mutations write exactly one usage audit row in their own transaction (#869); a request without authenticated audit metadata, or a rejected audit insert, changes nothing. Production wiring installs every operation, builds the enabled-administrator inventory from the published configuration under the lock, and refuses a reload that would lock out policy administration. Policy operations take their `If-Match` from the POLICY revision, not the configuration revision. The policy runtime singleton is registered in the usage migration ledger as `2026110704` (schema 15).

### Authorization compatibility

- The entire `router-internal:` prefix is reserved for internal authorization
  identities, not only `router-internal:compat:`. Policies or configured public
  subjects selecting that namespace fail startup/reload validation rather than
  being interpreted as public grants. Check existing offline policies and
  subject mappings before upgrading; do not use internal identities as
  application subjects.
- Basic authentication membership, password hash, subject and domain changes
  require a restart. Authorization policy reload does not itself refresh the
  Basic authentication directory; use a restart for Basic revocation/rotation.

## [4.7.1] - 2026-11-08

Patch release. Rebuilds v4.7.0 with the Go 1.26.10 toolchain floor for its
standard-library security fixes, and fixes `router-migrate --json` reporting
while a migration is pending. No configuration field, API surface, authorization
decision or usage-schema version changes: the usage schema stays at version 12,
so an upgrade from v4.7.0 runs no migration and a rollback to v4.7.0 needs no
database restore.

### Fixed

- `metrum-ai-router-migrate --json` prints the status document while a migration
  is pending, instead of an empty line with a zero exit. A pending migration is
  a migration definition that carries handler functions, which the encoder
  cannot represent; the status is now projected into a handler-free document
  whose pending entry carries only safe metadata (id, name, release, checksum,
  manifest digest, execution, maintenance, lock, timeout and rollback classes,
  data-job key, postcondition and dependencies). The rest of the document keeps
  its existing shape and field names, and an encoding failure is a non-zero exit
  with a bounded error (#807).

### Security

- v4.7.1 release binaries are built with Go 1.26.10, which carries the Go
  standard-library security fixes for the 37 package and symbol advisories
  recorded in #854 (GO-2026-4599 through GO-2026-6809; the Go vulnerability
  database lists them at https://pkg.go.dev/vuln/). Source builds require at
  least Go 1.26.10, and the CI, release and
  container build producers assert the version they actually run. A
  `govulncheck` scan of this tree on exactly Go 1.26.10 reports no package or
  symbol findings and one module-only advisory (GO-2026-5932,
  `golang.org/x/crypto/openpgp`) that router code does not call.

### Release verification

The live release gate ran on the release commit `2deb954d791d` with the v4.7.0
gate configuration unchanged (the archived baseline configuration, sha256
`41a3dc29…`, and the archived stage-C configuration, sha256 `cafaee3d…`), in one
sequential run so the timing-sensitive stages shared one pool. Every red cell is
reported as red:

| Stage | Result on 2deb954d | v4.7.0 result |
| --- | --- | --- |
| A: 24 agent cells (6 routed lanes and 2 direct baselines x file/edit/quote) | 20/24 | 19-21/24 across the replicated runs |
| B: Harbor matrix, routed vs direct in both API styles | pass, 5/5 both styles plus the offline replay | pass, 5/5 both styles plus the offline replay |
| C: live SDK compatibility suites | fail, fully classified | fail, fully classified |
| D: parallel tool calls, direct vs routed | pass, 6/6 | pass, 6/6 |

The four red stage-A cells are the three `openai-chat -> gpt` cells, where the
provider answers `400 upstream-rejected` for the `reasoning_effort` plus tools
request shape (the documented limitation, identical on v4.7.0), and
`routed-openai-responses-claude/quote`, which failed on admission `429
tpm-exceeded` at the unchanged caller quota, the same quota class v4.7.0 recorded
as moving between cells from run to run. Stage C's tester reports are identical
to the archived v4.7.0 reports apart from request ids and the local port:
documented `501` surfaces (`/v1/complete`, moderations, message batches, beta
files, `responses compact` and `responses input_tokens` on the Claude target) and
typed `400` refusals (embeddings without an embeddings group, document modality,
thinking and chat on the GPT target). No router defect appears in the
classification, and the stage is not reported as a pass. Stage B passed the
bounded Harbor matrix on a dedicated Docker host: every lane 5/5, routed equal to
direct on every task, and the offline replay passed before any provider call.

Upgrade path, with the released v4.7.0 binaries: v4.7.0 creates a usage database
(schema 12); v4.7.1 opens it, reports a fresh ledger's 13 pending migrations in
the safe JSON plan, serves a request (usage rows 0 to 1) and finishes at
`schema_version=12 data_version=1 state=current pending=0 entries=13`; the
released v4.7.0 binaries still read that database afterwards.

This change set contains release documentation and one test-only change:

```
CHANGELOG.md
scripts/test_check_go_toolchain.py
docs-site/docs/release-notes/index.md
```

The test change is the #854 Go-floor note assertion: it read the `[Unreleased]`
section by name, which a release branch does not have once its notes become the
newest released section, so it now reads the newest section (still `[Unreleased]`
on main). It asserts the same two strings and no router behaviour.

`git diff --stat 2deb954d791d HEAD -- internal cmd go.mod go.sum` is empty, so
the router code is identical to the gated commit.

## [4.7.0] - 2026-11-06

Package release. Adds opt-in tool-aware routing, configurable HTTP deadlines with
a stream progress deadline, stored Responses affinity, and PII filtering on
translated instruction histories. The router hierarchy compatibility matrix is
now a required step of the release workflow, and layered edge → datacenter →
frontier deployment ships as a reference configuration with per-hop usage rows
and measured per-hop results on AMD Instinct MI325X, Intel Xeon CPU and Strix
Halo. Reference tiers validated on hardware are `nvidia-datacenter` (H100 80 GB)
and `amd-strix-halo-edge` (its measured 1-worker p95 is above the 8 s target, as
its Limits state); `nvidia-gb10-edge` is a functional check only;
`amd-mi350p-datacenter` is committed without a hardware run (#785); and the edge
bundle ships alongside them. Shared hierarchy governance is planned for v5.0.0.

**Usage schema moves from 11 to 12.** A v4.6.0 database is upgraded in place:
with the default `migration_policy: auto-safe` the router applies the shipped
migration at startup, keeps every existing usage row readable and continues
writing. Deployments that pin `migration_policy: deployment-job` run
`router-migrate --action apply` and `--action verify-serving` as documented in
`docs/DATA_MIGRATIONS.md`. No configuration field is removed or renamed in this
release.

### Fixed

- An OpenAI Chat target that refuses the output-cap field it was sent and names
  the other field in its `400` is retried once with the named field, so a model
  that requires `max_completion_tokens` works for a caller that sent `max_tokens`
  (and the reverse), instead of surfacing an opaque `upstream-rejected` error.
  The retry is bounded to that exact refusal, keeps one attempt row and records
  an `output_token_field_retry` trace event; `max_tokens` remains the unset
  default so compatible servers that require it keep working (#778).

### Added

- Configurable HTTP server deadlines: `server.read_timeout_ms`,
  `server.write_timeout_ms`, `server.idle_timeout_ms` and
  `server.stream_idle_timeout_ms` (zero selects the default; negative values and
  values above 24 hours are rejected). The absolute write default follows the
  worst-case attempt chain (largest attempt budget times the group's target
  count, at least the upstream timeout), and event streams get a progress
  deadline that every successful write extends. The stream idle window starts at
  the first data byte, so a slow first token is bounded by the attempt/upstream
  timeout while a stall after the first chunk ends the stream with a clear
  `upstream_timeout` record (#759, #760).

- Stored Responses affinity is new in v4.7: the authenticated-encryption
  envelope used for upstream ids has no earlier released format, so there is no
  stored-response metadata to migrate and no compatibility path with v4.6.0
  (#533).

### Added

- Opt-in `strategy: tool_aware` model groups with a `tool_routing` block. In
  `mode: observe` (the default) every other behaviour is unchanged: the router
  decodes the upstream's tool calls, validates them against the caller's
  arguments (JSON, the documented basic JSON-Schema subset, tool names and a
  forced `tool_choice`), records the first failure class per attempt, and keeps
  windowed reliability counters per target. `mode: enforce` adds one
  same-target retry and bounded escalation to the next eligible target when a
  call fails validation, stops at the request deadline and the effective spend
  ceiling, bills discarded attempts to the request's usage and quota, and
  delivers the last response unchanged when the budget is spent. Groups without
  the block never run this code. See `docs/TOOL_AWARE_ROUTING.md` (#436).

### Added

- Direct-provider differential canary (evaluation layer ARCH-05 of #330): fixed,
  locally checked probes sent to an exact provider/model and to the same model
  pinned through the router, with a report that reuses the evaluation report
  schema and fails on a router regression. Offline-tested in the PR gate; live
  runs stay opt-in and bounded (`make eval-canary`).
- Evaluation reports distinguish a pinned baseline from a separately reported
  current-stable canary (`provenance.baseline_kind`, `provenance.baseline_of`):
  the validator rejects a canary reported without its pinned baseline, so a
  current-stable client row can never replace the pinned one (AGENT-01, #330).

- `reasoning.with_tools` target metadata. Setting it to `false` declares that
  the target's outbound dialect rejects a reasoning control together with
  function tools, as OpenAI reasoning models do on Chat Completions. The router
  then refuses that combination before dispatch with `400 unsupported_capability`
  and `param` naming the caller's own reasoning field (`reasoning_effort`,
  `reasoning`, or `thinking`), with the bounded filter reason
  `reasoning-with-tools` in candidate telemetry. Unset keeps today's behaviour,
  so existing configurations do not change routing (#703).

- `count_tokens` target metadata for Anthropic Messages targets, default
  `true`. A target or gateway that serves Messages without implementing
  `POST /v1/messages/count_tokens` can declare `count_tokens: false`; the router
  then answers count requests from its local estimate, marked
  `X-Metrum-Usage-Source: estimated`, instead of calling an endpoint that does
  not exist (#714).

### Security

- PII filtering now also filters each translated instruction text at its
  transmitted boundary, not only the individual raw values. A pattern that
  matches only after a target assembles a system prompt from several messages or
  text parts (for example a Chat caller's leading system messages joined for a
  Responses or Anthropic target) is replaced with a placeholder before dispatch.
  Operator scopes still govern it: a translated instruction is filtered when
  either `apply_to.system` or `apply_to.messages` is enabled. Scope ownership
  also follows the value, not only the field: `apply_to.system` now covers
  instruction text in `system`/`developer` messages and in `content` fields on
  those items, and `apply_to.responses_input` covers the Responses `input`
  array and its items. Both rules only widen redaction for configurations that
  already enabled a scope.
- Responses system-message histories are now served by Anthropic and Bedrock
  Messages targets on PII-enabled groups instead of failing closed: the
  assembled instruction is filtered at its transmitted boundary, so
  `redact_only` sends placeholders, `fail_on_match` blocks before dispatch, and
  response-cache identity includes the transmitted instruction. A group that
  disables both `apply_to.system` and `apply_to.messages` still fails closed with
  `system_pii_unsupported` because the router cannot redact that instruction.
  Responses developer-role histories remain excluded (`developer_pii_unsupported`)
  (#645).
- Response-cache identity for translated targets now includes the exact
  instruction text the target receives, so two requests whose normalized
  representation is identical but whose transmitted instructions differ can no
  longer share a cache entry (#645).

### Fixed

- `metrum-ai-routerctl callers generate --write` merges the minted caller by
  caller id instead of replacing the table, refuses to overwrite an existing
  caller unless `--replace` is passed, and no longer writes a token file for a
  caller it refused. Previously a second mint for the same owner, project and
  environment silently replaced the first caller, so the earlier token started
  answering `401` (#755).
  **Behaviour change for scripts:** re-minting a caller with the same
  `<owner-user>-<project>-<env>` id, for example to rotate its key, now requires
  `--replace`; without it the command exits with an error and changes nothing.
  Use `--replace` to rotate that one caller, a distinct
  `--owner-user`/`--project`/`--env` to add a separate caller, or
  `callers rotate --id …` when only the verifier should change.
- `POST /v1/messages/count_tokens` no longer surfaces `404 upstream-rejected`
  when a gateway has no count route. An upstream `404` whose body is not the
  provider's own typed `not_found_error` is answered with the router's local
  estimate and the `estimated` marker, with a bounded trace event and no
  provider/model/target identifiers in caller text. A typed provider
  `not_found_error` (an unknown or misconfigured model) keeps its existing
  `404 upstream-rejected` class, and every other provider status keeps its
  class too (`401`/`429` stay provider 4xx, `5xx` becomes `502
  upstream-failed`) (#714).

- Per-target routing filter reasons are stored even when `record_candidates`
  is `false`. The candidate rows and the bounded reason rows now have
  independent switches, so a pre-dispatch eligibility refusal keeps its
  `request_target_filter_reasons` evidence; with no candidate rows the
  reason's `candidate_index` is the position of the target in the group's
  `targets` list (#716).

- Unary Responses responses now encode the caller-facing `id`, output item ids
  and call ids, matching the streamed surface, **only** when stored-response
  affinity is enabled for the requested group. A stock deployment with affinity
  off keeps its existing caller-visible ids and bytes unchanged (#533).
- When stored-response affinity is configured for any model group, a
  `previous_response_id` sent to a group that does not enable it is refused with
  `400 stored_response_unsupported` instead of forwarding the provider id, so
  ownership cannot be bypassed through another group that uses the same account
  (#533).

- Owner-bound stored Responses affinity for model groups listed in
  `server.responses_affinity.enabled_groups`, off by default. For a stored
  native Responses request the router records keyed, encrypted affinity
  metadata (owning caller-token fingerprint, keyed response lookup hash,
  provider/account/model/dialect identity, lifecycle and expiry) before the id
  is exposed, and a `previous_response_id` follow-up is pinned to the recorded
  account instead of being re-routed. A missing, expired or other-caller id
  returns the same `404 response_not_found` before any upstream call, and an
  unavailable pinned target returns `409 response_target_unavailable`. With no
  group enabled no row is written, no lookup runs and request/response bytes
  are unchanged. Retention purge, legal holds, get/delete/input-items/cancel
  and docs follow in later reviewed changes (#533).

- Add the dormant `response_affinity` usage table in schema 12 as the
  foundation for owner-bound stored Responses. It stores only keyed lookup and
  token fingerprints, protected upstream ids, the exact provider/account/model
  identity, tenant/project/environment and local lifecycle metadata, with
  uniqueness scoped to the owner domain; it has no bearer-token, prompt or
  response-content column. The migration enables no stored Responses endpoint,
  writes no rows and changes no request behavior: stored Responses affinity
  stays off by default and is enabled per model group only by later reviewed
  changes. Back up before the migration gate and restore a pre-upgrade backup
  to downgrade (#665).

- Serve the stored Responses operations for enabled affinity groups:
  `GET /v1/responses/{id}` (including `stream=true`), `GET
  /v1/responses/{id}/input_items` and `DELETE /v1/responses/{id}` are forwarded
  to the recorded provider account after the same owner, expiry and target
  checks as continuation. A missing, expired or other-caller id returns the same
  `404 response_not_found` before any upstream call; an unavailable target
  returns `409 response_target_unavailable`; DELETE records a local tombstone and
  the confirmed (or not-found) upstream deletion state. Caller-visible ids are
  rewritten on these surfaces under `identifiers.mode: rewrite`. With affinity
  disabled these routes keep the documented `501 unsupported_endpoint` behavior
  (#533).

- `POST /v1/responses/{id}/cancel` is served for enabled affinity groups with
  the same owner, expiry and target checks as the other stored-response
  operations, forwarding the cancellation to the pinned provider account and
  recording the local `cancelled` state. Unknown, expired and other-caller ids
  keep returning the same 404 before any upstream call; affinity disabled keeps
  the documented `501 unsupported_endpoint` behavior (#533).

- Stored Responses affinity metadata now follows the retention policy: enabling
  the feature requires `server.retention` to be on, a `responses_affinity`
  retention class is provided (defaulting to the configured local TTL, 30 days)
  and the existing retention job purges expired affinity rows in bounded
  batches. Legal holds on that class block deletion by their time range, and the
  expiry check still makes a row unusable as soon as its local TTL passes.
  Releasing a hold makes the row eligible again; neither expiry nor purge claims
  upstream deletion (#533).

### Release verification

The live release gate ran on the release candidate `5db5ac66a7e4` with the
v4.6.0 gate configuration unchanged, recorded as issue #805. Its results, with
every red cell reported as red:

- **Stage A (agent lanes):** 24 cells per run. The only cells that fail with a
  non-quota reason are the three `openai-chat → gpt` cells, where the provider
  rejects the `reasoning_effort` plus tools request shape; the same failure
  appears on the previous release with the same configuration, and a replicated
  four-pair A/B on the same configuration and harness, with the decision rule
  fixed before the replicate runs, shows the candidate's failure set equal to or
  smaller than the previous release's.
- **Stage B (Harbor matrix):** routed equals direct at 5/5 in both API styles,
  plus the offline replay.
- **Stage C (live SDK suites):** reported as a red stage with a full
  classification: documented `501` surfaces and typed `400` refusals, with no
  router defect in the classification.
- **Stage D (parallel tool calls):** 6/6, direct and routed.

This change set contains release documentation only:

```
 CHANGELOG.md                          | 54 ++++++++++++++++++++-
 deploy/reference/README.md            |  8 ++--
 docs-site/docs/release-notes/index.md | 88 +++++++++++++++++++++++++++++++++++
 3 files changed, 145 insertions(+), 5 deletions(-)
```

`git diff --stat 5db5ac66a7e4 v4.7.0 -- internal cmd go.mod go.sum` is
empty, so the router code is identical to the gated commit.

## [4.6.0] - 2026-11-02

Security, PII and tool-compatibility release. No usage-database migration is
included: the usage schema stays at version 11, so an upgrade needs no migration
gate and a rollback to 4.5.0 needs no database restore for schema reasons.

### Added

- Reasoning provenance binding for opaque reasoning history, **off by
  default**. When enabled, the router binds each provider-owned
  `encrypted_content` value and each Anthropic `thinking`/`redacted_thinking`
  block to the dialect, provider account, model and group that minted it,
  unwraps it for the matching target and drops it everywhere else, so a foreign
  value is never replayed after a fallback. `mode: off` changes no bytes;
  `observe` and `enforce` require a secret supplied through the named
  environment variable, and drop mismatched history with bounded diagnostic
  classes that carry no values. See
  `docs-site/docs/operations/reasoning-provenance.md` (#622).

- OpenAI-compatible embeddings. `POST /v1/embeddings` is served for model
  groups configured as embeddings groups, with admission control that rejects
  malformed, oversized or ambiguous input before any upstream call, and usage
  metering on the same accounting path as chat. Configuration is fail-closed
  (`embedding_space`, `embedding_dimensions`, `default_embedding_dimensions`):
  a group cannot mix embedding and non-embedding targets, every target must
  serve the same space and dimension set, and chat-shaped selection strategies
  are rejected so a selector cannot pick a different vector space (#532).

- CFO usage report: scoped spend and hypothetical savings by team, project, key
  and model group, with source-dated baseline prices, top consumers, an hourly
  trend, an explicit project-to-team mapping, and JSON, CSV and optional
  Markdown exports through the Admin API, `routerctl` and a browser view.
  Recorded costs are usage-derived, not invoices; live token quotas are
  labelled separately and unmapped usage stays separate rather than being
  attributed (#435).

- New usage rows record a bounded endpoint label, covering Chat, Responses,
  Messages and the other surfaces that create usage events. The column and its
  migration are unchanged from schema 11 and historical rows are not
  backfilled (#653).

- Tool-call compatibility evidence for parallel tool calls: an offline fixture
  suite covering native and translated directions, three calls per turn with
  out-of-order, error, multi-part and empty results, and a budget-capped live
  gate against hosted providers. The published page records the cells that pass
  and the cells that remain unproven (#656).

### Changed

- Routing errors now name the reason that actually removed the target.
  `400` capability failures include the recorded eligibility reason and a
  `param` that is only set when every rejected candidate shares that field;
  otherwise `param` is null rather than guessed. Anthropic-shaped error
  envelopes gain `error.code` and `error.param`. Selection telemetry uses the
  same predicates (#655).

- Requests evaluated against a PII-enabled model group bypass the router
  response cache for both reads and writes while target-bound cache identity is
  reconciled, and report `cache-pii-filter` as the bounded reason. Other groups
  keep their existing caching. Upstream call volume and cost can rise for those
  groups (see the operator notes for the cutover limits) (#670).

- Candidate shadow replay is skipped for requests evaluated against a locally
  active PII-enabled group, with the policy rechecked before dispatch, and
  Responses system-message histories on PII-enabled groups fail closed with
  `system_pii_unsupported` when the only eligible translation target is
  Anthropic or Bedrock Messages. Both guards can reduce eligible targets and
  reduce upstream calls (#664, #668).

- A null Anthropic Messages `context_management` is treated as absent and is
  not forwarded upstream. Non-null values still require both the caller's beta
  and the target allowlist (#459).

- `POST /v1/content_provenance_checks` returns an authenticated OpenAI-shaped
  `501 unsupported_endpoint` instead of falling through to a generic
  unsupported response. The operation is not implemented and nothing is
  dispatched upstream (#620).

- Unsupported-surface documentation no longer describes the older `502` for a
  missing eligible target; capability failures are `400 unsupported_capability`
  (#654).

### Fixed

- An explicitly empty tool result is preserved as an empty string when OpenAI
  Chat or Responses history is translated to Anthropic Messages, instead of
  being serialized as `null` (#657).

- Multi-part tool-result text is concatenated exactly, without inserted
  separators, on Messages→Chat, Messages→Responses and Chat→Responses, so a
  result split into `alpha` and `beta` arrives as `alphabeta`. The same rule now
  applies to Bedrock Converse conversion. Ordinary message formatting,
  mixed-media results and identifiers are unchanged (#672, #678).

- Mixed text and image content inside a tool result is preserved when
  Messages history is translated to Responses; it previously produced an empty
  tool output with a `200` response (#679).

### Known limitations

- A Chat Completions caller that combines tools with `reasoning_effort` is
  affected, not only an Anthropic caller. A Chat-dialect target lets the provider
  reject it (`400 upstream-rejected`); a Responses target behind the
  `chat_to_responses` bridge reports `400 unsupported_endpoint_for_model` with
  `param: model` for a target that does not declare `openai-chat` in
  `supported_inbound_dialects`; declaring it makes the Chat caller eligible.
  Avoid tools plus `reasoning_effort` from a Chat caller against a Chat target
  until #703 lands (#697).

### Release verification

The gate ran on `0ea47c4ae10d4793a4afec56ad509e71634bac72`. This commit differs from it only in the release notes and the gate scripts (`scripts/release_gate.py`, `scripts/release_gate_test.py`, #713 and #715); router code is identical, verified with `git diff --stat 0ea47c4a <tag> -- internal cmd go.mod go.sum`.

### Security

- Example Kubernetes overlays are scanned as rendered manifests rather than as
  patch fragments: a required CI lane renders every overlay plus a composed
  Linkerd example and fails on HIGH or CRITICAL findings with a reviewed,
  reason-bearing ignore list. The example vLLM serving workloads and the
  example runner image run as non-root with dropped capabilities, a read-only
  root filesystem and explicit writable mounts (#669).

- Dependency refresh removes the remaining high-severity advisory in the
  example evaluation lockfile (`basic-ftp`) and moves `glob`, `@hono/node-server`
  and `minimatch` to patched releases across the example and compatibility
  projects, including the documentation site through a small callable adapter
  that keeps its static server working (#682, #640). One advisory with no
  patched release remains tracked (`deepmerge`, documentation build only).

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
  (`benchmarks/overhead/evidence/`). The exploratory 90%-floor profiles are
  published as exploratory. Strict continuous occupancy for enforce/32, CPU and
  memory per 1,000 requests/s, maximum sustained throughput and the edge case
  remain unproven, and the issue is closed with those limits recorded. Rerun:
  `make overhead-profile-smoke`, `make overhead-decision-smoke`.

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

- End-to-end agent gate on the release commit: the pinned agents run in OpenAI
  and Anthropic styles against GPT and Claude targets, Harbor runs in both
  styles, and the full live SDK suites run with per-suite classification
  (`docs-site/docs/evaluation/api-compatibility-live.md`, which also records the
  omitted suites and states that omitted suites are not passes).

### API compatibility

- A leading text-only Responses developer message becomes the Anthropic system
  prompt; a late or non-text developer message still returns `400` (#642).
  Multipart developer content is rejected rather than silently dropped.
  Developer histories are ineligible for Anthropic targets when the model
  group's PII filter is enabled, are not shadow replayed, and bypass the
  response cache when an Anthropic target can serve. Existing encrypted-reasoning
  and retention limitations still apply; PII and cache alignment is deferred to
  #645.

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

- Opt-in cross-dialect reasoning bridges OpenAI ↔ Anthropic, in both directions
  and in streaming: `reasoning_effort` maps to Anthropic thinking budgets of
  1,024 / 4,096 / 16,384 tokens (capped below the outgoing `max_tokens`) or to
  adaptive thinking when the target is explicitly configured for it, and
  Anthropic budgets map back to low / medium / high effort with adaptive
  counting as medium. Only visible summaries cross the bridge; a
  provider may omit them (#613, #614, #633).

- Cross-dialect translation strips Anthropic signatures, redacted thinking,
  OpenAI encrypted reasoning and opaque foreign reasoning history, and foreign
  reasoning items are dropped on continuation, so exact reasoning-state
  continuity is not guaranteed. Native same-dialect passthrough still carries
  signed and redacted reasoning; native Anthropic passthrough removes only
  `thinking` blocks whose signature is empty or missing. The Bedrock Converse
  adapter cannot encode signed or redacted thinking and returns `400
  unsupported_block` before dispatch; a real Converse codec (#635) and
  provider/account provenance (#622) are deferred to v4.6.0 (#633).

- Responses tool history is preserved across the Chat and Anthropic bridges,
  including parallel tool calls and array-shaped tool outputs (#617, #618), and
  a translated tool result that arrives as a text array keeps its ordered text
  blocks instead of being flattened with an invented newline (#630, #636).

- A Responses image `detail` hint is ignored for Claude targets rather than
  rejecting the request, restoring the cross-dialect vision path (#637), and an
  explicit `reasoning_effort: none` is honoured as "no reasoning" instead of
  being rejected, while an unrecognised effort value returns `400
  invalid_request_error` with `param: reasoning_effort` (#638).

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
