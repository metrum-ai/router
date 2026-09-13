# Learned Routing Policy operator runbook

Customer-facing explanation:
[Learned Routing Policy](https://llm-api.apps.metrum.ai/docs/routing/learned-routing-policy)
(source: `docs-site/docs/routing/learned-routing-policy.md`). This file remains
the operator/maintainer source of truth for train, serve, promotion, and
rollback procedure.

Different models complete different jobs to different degrees. The objective is
the cheapest model or mix that still completes the workload, established by
objective outcomes. LRP trains per-target quality and output-token models and
uses the router's external-policy interface to recommend a target above a chosen
quality floor. Unit tests, extraction accuracy, OCR targets, tool correctness,
browser tasks, golden datasets and product acceptance tests can establish those
outcomes; Harbor is one possible agent harness.

## Worked scenario: choose, measure, learn, then serve

Treat a model group as a workload quality and cost contract. Decide which jobs
belong in the group before choosing a training algorithm. The Python/uv project
in `services/learned-routing-policy` uses LightGBM for per-target quality and
output-length models, isotonic calibration for quality, and a shared embedding
and scalar feature implementation for training and serving. It trains the
**routing policy**, not the upstream language models.

### 1. Define the jobs and their acceptance tests

Inventory representative requests, including uncommon failures and expensive
cases. Separate independent conversations before splitting data. Match the real
language, prompt length, system instructions, tool schemas, structured output,
output limits, and client dialect distribution. A collection of easy prompts
cannot establish quality for large tool-bearing coding requests.

| Workload | Dataset should include | Outcome that defines a sufficient answer |
|---|---|---|
| Short calculations or classification | Easy and ambiguous cases, exact output constraints | Exact result or approved classification label |
| Structured extraction | Missing fields, distractors, representative document sizes | Schema validation **and** correct field values |
| Code review or repair | Multiple files, realistic context and tool requirements | Objective tests or a reviewer-recorded acceptance result |

These are workload-design examples. The v1 fanout client replays OpenAI Chat;
it records other dialects as ineligible. Validate Responses, Anthropic, tools
and bridge shapes independently before exposing them through an enforced group.
Image-bearing requests pass through to first eligible order in v1. Keep those
separate from text-only learned-quality evidence.

### 2. Choose upstream candidates and a reference target

Select candidates that could meet these outcomes at different costs. Record the
exact provider, served model ID, API skin, tool/modality support, context and
output-cap behavior, and current prices. Catalog an unvalidated model without
activating it. Run direct upstream and router-level smokes before a candidate
joins an isolated calibration group. Price alone does not establish suitability.

Choose an anchor to define the comparison bar and, when possible, a separate
judge to avoid self-preference. A verifier with known ground truth takes
precedence over the anchor comparison. Keep one restricted, single-target router
group per candidate and one reusable caller authorized for those groups. This
ensures a fanout row identifies the target that actually answered. Confirm the
router's selected target using authenticated decision readback.

The `--targets` YAML describes the actual upstream ID separately from its catalog
alias. The following is a shape template: replace every model, alias and group
with your validated deployment values and add one row per candidate.

```yaml
targets:
  - provider: openrouter
    model: vendor/validated-model
    model_ref: candidate-a-catalog-alias
    router_group: candidate-a-calibration
    honors_max_tokens: true
    output_token_field: max_tokens
    router_targets:
      - provider: openrouter
        model: vendor/validated-model
        model_ref: candidate-a-catalog-alias
```

The exported `router_targets` list must contain exactly that candidate identity.
The current fanout price refresher uses exact OpenRouter model metadata even
when sending through the router. Other hosted/private price sources require an
adapted collection integration; they cannot be inferred from this template.

### 3. Collect outcomes before training

Put the approved dataset and outputs in private storage, then use the pipeline
commands below. Each request row carries a stable request/session key, workload
group, messages, shape metadata, cap and optional verifier specification. Never
reconstruct content from metadata-only router logs. Select an approved content
capture export or a dataset you are authorized to process.

Fanout produces one response row per request/candidate with status, usage,
request-time pricing, latency and router request ID. Inspect unsuccessful and
ineligible rows before judging. Missing prices, unsupported shapes, unhonored
caps and missing usage are explicit evidence gaps. Expanding an output cap does
not make a target honor it. Keep unknown costs visible.

Run deterministic verifiers in the isolated rootfs, or explicitly approve
third-party judging. Do not send private workload material to a judge merely
because fanout was approved. Missing verifier infrastructure or invalid judge
output produces missing evidence. It must not become a successful training
label. Preserve outcome method because verifier success, pairwise preference
and rubric scores answer different questions.

### 4. Train and assess the held-out workload mix

`featurize` builds the same vector used at inference. Optional
`--near-dup-cosine` (reviewed value `0.98`) drops later near-duplicate
embeddings before the session split so templated prompts cannot leak across
partitions; see [LRP evaluation splits](./LRP_EVAL_SPLITS.md). `train` fits a quality
model and output-token model for each candidate, then calibrates on validation
rows. `eval` uses only the session-disjoint test partition and reports
serving-provider outcome/cost/latency variance for aggregator backends when
fanout recorded `serving_provider`. At least 200 training
rows are required per trained target; this minimum is not evidence that the
dataset covers your intended workload.

Inspect all six baselines, per-target coverage/calibration, outcome methods and
the quality-floor sweep. Compare total stored cost and user-visible performance,
not just the fraction of requests routed cheaply. A large expensive workload
can dominate spend even when half of all requests use the cheapest target.
Offline `eval` shares the composed decision path with serving (project floors,
latency evidence, cache evidence, pins, and abstention). It disables stochastic
exploration intentionally and records that limit. When a configured behavior
cannot be evaluated from the supplied data, the report marks
`evaluation_applicability` unsupported and promotion cannot pass for that
configuration. Failed promotion gates remain failed; synthetic CI success means
the pipeline and checks ran successfully, not that the learned policy is ready
to promote.

### 5. Load the bundle and explain actual decisions

Start `lrp serve` with the validated bundle, matching group config and
`LRP_POLICY_AUTH_HEADER`. Begin with router-native shadow. In enforced mode:

1. The router applies caller access, PII filtering and request-shape eligibility.
2. LRP computes features and calibrated quality/output-token predictions for
   the current eligible targets. No training or third-party judge runs here.
3. It compares estimated costs among targets whose quality meets the floor:
   `(estimated input tokens × input rate + predicted output tokens × output rate) / 1,000,000`.
4. LRP returns payload-local indexes and a safe label. The router calls the
   selected upstream and records actual usage, cost, latency and any fallback.

For a controlled local investigation, start the service with `--enable-admin`.
The authenticated `/explain` endpoint accepts the same normalized external-policy
payload as `/route`, not an OpenAI Chat request. It reports each target's
prediction, estimated cost, floor and top feature contributions. It is a separate
inference call: bind the same bundle/config/payload and disable exploration and
pins before comparing its decision with a previous request. Never export that
payload from a real deployment into public evidence.

Use SQLite/PostgreSQL scalar decision rows to distinguish the recommendation
from what served: `request_policy_executions` gives label/outcome/duration;
`request_routing_decisions` gives the configured decision;
`request_attempts` identifies the successful upstream and attempts;
`request_fallback_transitions` explains failover. Native shadow records
`shadow_recommended` plus the `shadow_recommended_candidate` routing signal,
while the configured first eligible target serves. Model feature contributions
explain a quality score locally; they are not a natural-language proof that an
upstream understood or successfully completed the task.

### Reproduce the complete synthetic scenario

The checked-in demo has two deliberately controlled jobs: short arithmetic and
long synthetic code-review context. The mock `cheap` target passes short jobs
only; `strong` passes both. Mock verifier outcomes and synthetic embeddings make
the example deterministic. LightGBM training, bundle loading, LRP HTTP inference,
the normal licensed router binary and relational decision telemetry are real.

```bash
uv sync --project services/learned-routing-policy --locked
# Choose a fresh private directory outside this checkout for each changed dataset/verifier.
uv run --project services/learned-routing-policy --locked python scripts/run_lrp_synthetic_demo.py --out-dir /var/tmp/lrp-worked-example
# Read the protected evidence.json locally and set LRP_BUNDLE_DIR to its bundle value.
uv run --project services/learned-routing-policy --locked python scripts/run_lrp_e2e.py --bundle "$LRP_BUNDLE_DIR" --cheap-model cheap --strong-model strong --explain-evidence --output /var/tmp/lrp-worked-example/public-inference.json
```

The optional explanation observer forwards the actual router payload to LRP and
calls authenticated `/explain` with that same payload in memory. It exports
only scalar predictions, costs, model feature identifiers/contributions and
actual selected-target readback. The 500-request latency measurement continues
to call LRP directly; observer/explain overhead is excluded from that measurement.

## Routing performance benchmark harness (issue #159)

Offline harness only. It does not execute live or paid benchmarks.

```bash
make lrp-routing-benchmark-harness-test
python3 scripts/lrp_routing_benchmark.py plan
python3 scripts/lrp_routing_benchmark.py config-patch --config B
```

Artifacts and schema live under
[`docs/evidence/learned-routing-policy/`](evidence/learned-routing-policy/).
Configurations A (static), B (external shadow), and C (external enforce) are
supported. Configuration D (GPU LRP / Shadeform topology) is deferred to
issue #162. Throughput mode marks deterministic local-sink rows
`synthetic_upstream`. Router-added latency is
`request_trace_events.router_upstream_wrote_request.duration_ms`:
receive clock and `router_receive` at
`internal/router/service.go:1318-1352`; `httptrace.WroteRequest` and
`router_upstream_wrote_request` at `internal/router/service.go:2209-2230`.

Review `public-training.json`, `public-training.log` and `public-inference.json`
as the explicit shareable artifact set. The first two contain projected scalar
training/evaluation evidence; the third contains test outcomes and the two actual
inference examples. The original `evidence.json`, datasets, responses, judgments,
features and bundles remain private and are **not** upload artifacts. Keep raw
test logs private unless a separate redaction review approves them.

The [recorded worked-example evidence](LEARNED_ROUTING_POLICY_EVIDENCE.md) shows
the training split, held-out baselines, actual selected upstreams and why the
cost promotion gate fails despite correct routing in this synthetic scenario.
Repeat this workflow on approved representative workloads with real embeddings
and independently validated upstreams before the manual promotion steps below.

## Installation and protected storage

Use Python 3.12+ and uv. From the product checkout:

```bash
uv sync --project services/learned-routing-policy --locked
uv run --project services/learned-routing-policy lrp --help
make lrp-test
make lrp-synthetic-demo
```

Set `LRP_DATA_DIR` to an operator-owned directory **outside** the public product
tree, restricted to its operator. All content-bearing data, responses, judgments,
features, model artifacts and evaluation details stay there. Do not commit
captured content, caller IDs, credentials or real model outputs. Small checked-in
fixtures are synthetic. Use a unique run marker when router caching is enabled;
authenticate selection readback and match actual router request IDs. Policy
payloads themselves contain no router request ID.

Secrets come only from environment: `OPENROUTER_API_KEY`, `LRP_ROUTER_TOKEN`, and
`LRP_POLICY_AUTH_HEADER`. The last is the secret value for fixed `X-LRP-Auth`, not
a name/value pair. Never pass secrets as CLI arguments or print them. The service
discards caller IDs/token IDs and retains only project/environment for optional
session heuristics. Model files and native inference libraries are trusted code
and artifacts; validate their source and dependency/container scan results.

## Pipeline commands

These commands assume `LRP_DATA_DIR` is already set, the input files are approved,
`LRP_VERIFIER_ROOTFS` has passed the isolated-worker preflight when verifiers are
used, and `lrp` is invoked with `uv run --project services/learned-routing-policy`.
Adapt `services/learned-routing-policy/targets.example.yaml` into protected
`targets.yaml` only after validating its deployment-owned single-target groups.

```bash
lrp collect --dataset "$LRP_DATA_DIR/seed.ndjson" --approved-content --out "$LRP_DATA_DIR/requests.ndjson"
lrp collect --router-log "$LRP_DATA_DIR/requests.jsonl" --content-capture "$LRP_DATA_DIR/approved-export.ndjson" --approved-content --out "$LRP_DATA_DIR/requests.ndjson"
lrp import-usage --db "$USAGE_DSN" --out "$LRP_DATA_DIR/explore-meta.ndjson"
lrp collect --usage-db "$USAGE_DSN" --content-capture "$LRP_DATA_DIR/approved-export.ndjson" --approved-content --out "$LRP_DATA_DIR/requests.ndjson"
lrp fanout --requests "$LRP_DATA_DIR/requests.ndjson" --targets "$LRP_DATA_DIR/targets.yaml" --via router --base-url http://127.0.0.1:8080/v1 --refresh-pricing --approved-content --out "$LRP_DATA_DIR/responses.ndjson"
lrp judge --requests "$LRP_DATA_DIR/requests.ndjson" --responses "$LRP_DATA_DIR/responses.ndjson" --anchor-provider openrouter --anchor anthropic/claude-sonnet-4.6 --judge anthropic/claude-sonnet-4.6 --approved-content --sandbox-rootfs "$LRP_VERIFIER_ROOTFS" --out "$LRP_DATA_DIR/judgments.ndjson"
lrp featurize --requests "$LRP_DATA_DIR/requests.ndjson" --embedding-model "$LRP_DATA_DIR/embed/model.onnx" --tokenizer "$LRP_DATA_DIR/embed/tokenizer.json" --out "$LRP_DATA_DIR/features.parquet"
lrp train --features "$LRP_DATA_DIR/features.parquet" --judgments "$LRP_DATA_DIR/judgments.ndjson" --responses "$LRP_DATA_DIR/responses.ndjson" --embedding-model "$LRP_DATA_DIR/embed/model.onnx" --tokenizer "$LRP_DATA_DIR/embed/tokenizer.json" --anchor-provider openrouter --anchor anthropic/claude-sonnet-4.6 --out "$LRP_DATA_DIR/bundles"
# Set LRP_BUNDLE_DIR to $LRP_DATA_DIR/bundles/<bundle_version printed by train>.
lrp validate --bundle "$LRP_BUNDLE_DIR"
lrp eval --bundle "$LRP_BUNDLE_DIR" --features "$LRP_DATA_DIR/features.parquet" --judgments "$LRP_DATA_DIR/judgments.ndjson" --responses "$LRP_DATA_DIR/responses.ndjson" --config "$LRP_DATA_DIR/lrp.yaml" --out "$LRP_DATA_DIR/eval_report.json"
lrp serve --bundle "$LRP_BUNDLE_DIR" --config "$LRP_DATA_DIR/lrp.yaml" --port 18093 --admin-port 18094
```

Router JSONL contains metadata only. It cannot supply prompts or automatically
replay explore-tagged requests. Content capture is governed encrypted storage;
an operator must supply an approved export. The read-only
[usage import](LRP_USAGE_IMPORT.md) joins `request_usage` and
`request_policy_executions` for explore labels such as `lrp:explore`; it still
cannot invent content and must be paired with an approved content join.
Non-synthetic collection, fanout and
judging require `--approved-content` after the operator approves that transfer.
Judging sends request and candidate/anchor content to a third-party model.
Use an appropriately authorized judge and data-retention policy. Prefer a judge
outside the candidate set; self-judging is explicitly reported even with swapped
positions. Verifier outcomes, pairwise preference and absolute rubric scores
have different semantics and must be assessed separately. Parse failures and
unavailable verifier infrastructure are missing evidence, not successful outcomes.

Arbitrary pytest execution is disabled unless an explicitly configured isolated
worker enforces no network, no host files/secrets, an unprivileged UID, bounded
scratch/CPU/memory/PIDs/output, and whole-process-tree timeout cleanup. A host
subprocess and temporary directory alone do not provide isolation. Never install
requirements or execute shell commands supplied by a dataset.

The [isolated verifier guide](../services/learned-routing-policy/lrp/judge/README.md)
provides the digest-pinned rootfs builder and mandatory no-skip preflight. Pass
`--sandbox-rootfs "$LRP_VERIFIER_ROOTFS"` to `lrp judge` after that preflight.
The serving container is separate from the offline verifier worker. Extensible
`sql_result` and governed `plugin` contracts are documented in
[LRP_VERIFIERS.md](LRP_VERIFIERS.md). Sample pairwise/absolute judgments for
protected human review with [LRP_HUMAN_JUDGE.md](LRP_HUMAN_JUDGE.md).

Use a local trusted int8 ONNX export of
[BAAI bge-small-en-v1.5](https://huggingface.co/BAAI/bge-small-en-v1.5) with its matching
tokenizer. The manifest binds artifact hashes, feature order, embedding settings
and target model identities. No runtime download or silent synthetic fallback is
allowed. Train and serve share normalization of system plus recent turns and
truncate from the end to 512 tokens. Session-hash splitting keeps sessions out
of multiple partitions. Predictions below 200 training rows are excluded.

## Candidate targets, pricing and caps

The six historical OpenRouter candidates from issue #15 remain a **catalog-only
experiment** until exact account/model/dialect smokes pass. Actual upstream model
IDs are `model`; catalog aliases are `model_ref`. Always refresh the
[OpenRouter model metadata](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties)
for live runs and store the fetched prices with each response. A missing exact
Nitro ID or price requires operator resolution; do not infer zero or silently use
another model. The shipped sample config is historical catalog evidence, not
current cost authority. Routing weights belong to group targets.

Router fanout uses one restricted, single-target group per candidate and one
deployment-owned caller with access to those groups. Record router request IDs
and selected targets. Positive caps filter targets marked `honors_max_tokens:
false`, irrespective of cap size. For the historical set this affects MiniMax,
Qwen and Grok, not Sonnet. Capped rows are ineligible. An uncapped experiment
requires explicit `--allow-uncapped`, separate spend safeguards and an operator
budget with `--concurrency 1 --max-total-cost-usd <approved-limit>`. A spend guard
stops subsequent requests; it cannot guarantee the cost of an already running
uncapped request. Retry costs and actual
reported billing remain distinct from terminal usage-times-price estimates.
Reports use stored prices/costs and include TTFB, duration, throughput and errors
so both cost and slow user experience can be investigated.

## Routing and deployment

Start from `services/learned-routing-policy/lrp.example.yaml`. Group names are
deployment-defined. Keep exploration and pinning disabled initially. Opt-in
exploration requires both a nonzero rate and an allowed calibration project.
Optional TTL/LRU pins hash group/project/environment/first user text in memory;
identical first prompts in a shared project can collide. This is a heuristic,
not an authenticated conversation identity. Training on PII-redacted data must
match the group's serving redaction distribution deliberately.

Configure the router group with `strategy: external`, `include_request: true`,
`url: http://127.0.0.1:18093/route`, `allow_hosts: [127.0.0.1]`,
`headers: {X-LRP-Auth: '${LRP_POLICY_AUTH_HEADER}'}`, and `mode: shadow`.
The commented sample shows the full configuration. LRP only selects from current
eligible targets, preserving indexes after router filtering. Unknown/undertrained
targets are excluded from learned predictions; with none known it returns first
eligible order with `lrp:no-known-target`. Unknown prices cannot win as free.
If known targets all fall below the configured training minimum, LRP returns
503 rather than re-enabling them. The router applies its configured policy-error
behavior. Conflicting dialect/catalog aliases for duplicate target identities
are rejected instead of silently sharing incompatible predictions.
Image requests pass through to first eligible order; no learned image-quality
claim is made. Missing request content degrades with `lrp:no-request-content`.

Optional Wave 2 selection constraints—per-project quality floors, upstream
latency p95 gates, evidence-based prompt-cache cost estimates, and bounded
explanation labels—are documented in
[LRP_SELECTION_CONSTRAINTS.md](LRP_SELECTION_CONSTRAINTS.md). They are off or
conservative by default in the sample config and do not authorize live route
activation.

### Choose a deadline for the workload or test

The LRP service's top-level `deadline_ms` accepts **1–4,500 ms**, with an unchanged
default of **200 ms**. Increase it explicitly when the workload accepts a longer
routing wait, or when a controlled test needs to observe slow learned inference
instead of the default BT fallback. For example, in the LRP service configuration:

```yaml
deadline_ms: 500
groups:
  workload-staging:
    quality_floor: 0.8
    explore_rate: 0
    pin_ttl_s: 0
    min_train_rows: 200
```

In the matching **router** group's `external_policy`, set `timeout_ms: 750` to
leave transport/serialization headroom. The router permits at most 5,000 ms;
keep its timeout strictly greater than the LRP deadline. These are separate
processes/configs, so validate the pair during deployment. A shorter router
timeout can terminate the policy call before LRP returns its own fallback,
invoking the router's policy-error behavior instead.

For a temporary test, the CLI override takes precedence over YAML and uses the
same range validation:

```bash
uv run --project services/learned-routing-policy --locked lrp serve --bundle "$LRP_BUNDLE_DIR" --config "$LRP_DATA_DIR/lrp.yaml" --deadline-ms 500
```

Omit `--deadline-ms` to use the configured value, or 200 ms when unset. The
override affects this service invocation; it does not modify the YAML or router
timeout. Integer values outside 1–4,500 are rejected. A regression test verifies
that a controlled 300 ms inference uses fallback at the default 200 ms and
completes learned inference with an explicit 900 ms allowance.

The deadline covers service request handling, including body receipt and the
remaining inference allowance for primary prediction, ensemble uncertainty, and
requested explanation work. Feature build runs once per request. Timed-out or
admission-rejected requests do not start a second embedding or ensemble compute.
Larger deadlines do not speed up inference or increase the bounded inference
worker count. Busy workers can still cause immediate BT fallback; timed-out work
retains its slot until it finishes.
After changing the deadline, test caller latency, the proportion of ordinary
learned labels versus `lrp:latency-fallback`, and router policy errors under the
intended concurrency. Keep body-size, worker and access protections enabled.
Restore `deadline_ms: 200` and the prior router timeout to roll back this tuning.

A longer test deadline is a behavior setting, not a revised performance result:
the measured 15/40 ms benchmark failures below remain failures. For example,
the tested 512-token one-thread feature latency might fit within 500 ms, but
that does not establish service/router performance on your workload or hardware.

The current router egress rules require same-network-namespace loopback for a
private LRP sidecar: use one Kubernetes pod or explicit Compose network sharing.
A separate private Docker/Kubernetes Service on port 18093 is rejected, even if
allowlisted. Nonlocal endpoints must meet the router's public-address/default-port
rules. Use HTTPS for nonlocal trusted infrastructure.

`/healthz`, `/readyz` and `/metrics` bind a separate **loopback** admin port.
Readiness requires a valid bundle and warmup. `--enable-admin` enables authed
`/explain`, `/admin/reload` and SIGHUP reload; candidate bundles validate/warm
before atomic replacement. In-flight requests retain their old bundle. Keep
router `/metrics` separately restricted to `metrics_admin` callers.

Optional Ed25519 detached signatures for `manifest.json` are documented in
[LRP signed bundles](LRP_SIGNED_BUNDLES.md). Use operator-owned keys, a trust
store for rotation, and pass `--trust` / `--require-signed` to `lrp serve` (and
`lrp validate`) so startup and every reload entry point enforce the same trust
policy. A present signature is never skipped silently.

## Validation, promotion and rollback

### Measured real-embedding limitation

The [recorded evidence](LEARNED_ROUTING_POLICY_EVIDENCE.md#real-bge-benchmark)
includes all four real BGE-small QInt8 scenarios. On the shared AMD Ryzen 7 5825U
development host, the default one-thread 512-token shape measured 304.524 ms p99
embedding and 312.003 ms p99 full features. Median full features were already
247.355 ms, exceeding the service's 200 ms deadline. Expect BT deadline fallback
for this tested shape; timed-out inference retains its bounded worker slot until
completion, so following requests can also fall back while slots remain occupied.

The four-thread comparison measured 140.146/161.322 ms p99 embedding/features for
512 tokens, still failing the 15/40 ms stage budgets. The service uses one thread;
this comparison neither changes that default nor establishes a production fix.
Short 26-token inputs met those stage budgets, but do not validate the whole
workload, HTTP/router overhead, quality-model inference overhead or concurrency.

The benchmark had request concurrency one, 50 warmups and 500 measurements per
stage, with affinity to four distinct cores. Other training/tests/harness jobs
ran concurrently on the host. Affinity did not reserve CPUs or isolate the run;
these observations cannot separate model cost from contention or predict
dedicated production performance. Do not silently shorten input truncation to
make a benchmark pass: representation changes need versioned features,
retraining and independent request-shape evidence. Keep long-input enforcement
behind a validated workload/hardware contract.

Run `make lrp-test`, `make lrp-synthetic-demo`, and `make lrp-e2e`. Synthetic CI
demonstrates wiring and evaluates gates; it never demonstrates provider quality,
real bge latency or live readiness. The JSON/Markdown evaluation compares LRP
with cheapest, anchor, target-weighted random, BT-only and oracle, including a
quality-floor sweep. Promotion requires quality at least 97% of anchor, cost at
most 60%, floor violation at most 10%, applicable AUC at least .70 with 100 test
rows per active target, and quality/cost dominance over BT-only. Single-class
AUC is N/A with reason. Unknown costs, uncertain labels and no-oracle-success
rows remain visible and cannot silently improve the gates.

Manual evidence steps:

1. Run direct fanout on 50 committed synthetic prompts and six exactly validated
   candidates (`--via openrouter --refresh-pricing`); require 300 rows and at least
   95% successful responses. These provider-backed rows remain protected.
2. Judge those rows with approved settings; record coverage and judge spend.
3. Train/evaluate at least 2,000 operator-owned requests with session-disjoint
   holdout. Review all gates, label semantics, floor sweep, language distribution
   and workload acceptance. Provider-backed evidence includes actual embeddings,
   upstream responses, objective/reviewer outcomes and selection readback.
4. Measure real pinned ONNX inference and router-observed p99 policy duration on
   a 4-vCPU runner: document warmup, 500 requests, sizes, concurrency and hardware.
   The targets are embedding p99 <=15ms and total policy p99 <40ms. Synthetic
   measurements cannot satisfy either claim.
5. Run 24 hours of native router shadow on a restricted staging group. Assert
   configured-order serving baseline and `shadow_recommended` telemetry. Native
   shadow does **not** preserve a former weighted distribution.
6. After review, enforce only on staging; compare stored costs, upstream and
   downstream performance, errors and fallbacks for seven days. Broad promotion
   requires independent Chat, Responses, Anthropic, tool/large-payload and any
   bridge/modality validation. Include security scan, redaction, metrics isolation,
   network controls, client acceptance, readiness, rollback and cleanup evidence.

Enable decision telemetry explicitly for validation. Inspect safe scalar
`request_policy_executions` duration/class label/outcome plus actual target and
attempt rows; persist no policy payload JSON. Stop promotion on failed gates or
compatibility evidence. Roll back policy influence with native `mode: baseline`
(configured eligible order), or deliberately restore the prior strategy/config.
Stopping LRP with `on_error: fallback` also serves configured order; it is not an
equivalent weighted rollback. `fail_closed` produces `502 routing-policy-error`
on policy failure. Keep previous validated bundles/config for atomic rollback,
then remove temporary calibration groups/callers through normal operator control.

Wave-2 uncertainty abstention, Thompson sampling, drift monitoring, and
cold-start seeding are documented in [LRP_UNCERTAINTY.md](LRP_UNCERTAINTY.md).
Those features default off and do not authorize live routing activation.

## Public seed corpus construction (#157)

Iteration-1 tooling under
[`services/learned-routing-policy/lrp/seed/`](../services/learned-routing-policy/lrp/seed/)
builds comparable turn labels from pinned public OpenHands trajectories.

### Worked limits (required reading)

1. **Teacher-forced continuation.** Replay prompts are fixed histories ending at
   the last tool or user observation. A plausible next assistant turn does not
   establish eventual task success. Later teacher-forced history is unchanged by
   candidate outputs.
2. **Valid alternative tool call.** Deterministic name and argument match measure
   agreement with the reference. A different but valid tool sequence can still
   advance the task. Keep verifier columns separate from judge and human labels.
   Do not treat a tool mismatch alone as a failed task.
3. **Cold versus warm observation.** Cache passes repeat the same turn and
   target. They do not create independent sessions for splitting. Count unique
   turns separately from replay rows. Pass 1 disables cache.
4. **Workload or target change.** Coding-trace evidence applies to the sampled
   coding workloads and listed targets. Changing models, prices, or moving to
   chat or image workloads requires fresh validation and may require retraining.
   First-run success with a reference bundle is not production readiness.

Spend estimates abort above 40 USD without an explicit approval flag. Unit tests
and default dry runs do not execute paid replay. See also
[LRP_EVAL_SPLITS.md](LRP_EVAL_SPLITS.md), [LRP_VERIFIERS.md](LRP_VERIFIERS.md),
and [LEARNED_ROUTING_POLICY_EVIDENCE.md](LEARNED_ROUTING_POLICY_EVIDENCE.md).

