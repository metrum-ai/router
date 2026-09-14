# Case study: learned upstream selection, training and inference evidence

This worked example demonstrates the full route from workload selection to
learned inference. Different models are sufficient for different jobs. The goal
is the cheapest sufficient model mix, established by objective outcomes rather
than model price or size alone. Start with the
[operator scenario](LEARNED_ROUTING_POLICY.md#worked-scenario-choose-measure-learn-then-serve)
for commands and the [caller-facing explanation](https://llm-api.apps.metrum.ai/docs/routing/learned-routing-policy)
for request behavior.

This snapshot records local validation on 2026-09-09. It contains **synthetic
workloads, mock upstream outcomes and a synthetic embedding**, with real
LightGBM training and real router/LRP processes. A separate real BGE embedding
benchmark appears below. No provider-backed quality, live shadow, live
deployment or broad promotion is established by these results.

## Public evidence files

| Artifact | What it contains |
|---|---|
| [public-training.json](evidence/learned-routing-policy/public-training.json) | Actual training duration, split/sample counts, package versions, calibration, six holdout baselines and every gate |
| [public-inference.json](evidence/learned-routing-policy/public-inference.json) | E01–E10/native-shadow results plus two actual request decisions, predictions, estimated costs, feature contributions and successful upstream readback |
| [public-bge-benchmark.json](evidence/learned-routing-policy/public-bge-benchmark.json) | Official model/tokenizer provenance and hashes, quantization, hardware/load qualifications and all four measured stage-latency scenarios |
| [public-tests.json](evidence/learned-routing-policy/public-tests.json) | Locally verified aggregate result: 79 tests, zero failures/errors/skips, actual verifier isolation required |
| [routing-benchmark.json](evidence/learned-routing-policy/routing-benchmark.json) | Issues #159/#162: ephemeral Shadeform CPU A/B/C (device=cpu) + keep-alive GPU A/B/C Harbor (device=cuda:0); paid portfolio reused. |
| [seed-example-corpus.json](evidence/learned-routing-policy/seed-example-corpus.json) | Issue #157 portfolio pivot: N=100 Shadeform paid portfolio (OpenRouter Qwen+MiniMax, Fireworks kimi, Baseten GLM; no OpenAI). |

Successful learned-routing validation workflows also attach the generated
`public-training.log`, containing only projected scalar training events. Its
captured excerpt is below; generated raw logs are not checked into the public
tree. Link the applicable workflow run from the pull request to establish the
current test result. A successful local result does not assert hosted CI passed.
The original `evidence.json` contains protected artifact paths and is not a
public attachment. Datasets, response-bearing reviews, credentials, full bundles
and raw policy payloads remain outside the repository.

## Candidate and workload contract

The deliberately controlled reference has two provider/model identities:
`synthetic/cheap` and `synthetic/strong`. Both have mock input/output prices;
`cheap` uses $0.10 per million tokens and `strong` uses $1.00 per million tokens.
These are test rates, not current upstream pricing. Both receive OpenAI Chat
requests capped at 128 output tokens. Exploration and session pinning are off
for the two worked inference examples; the quality floor is 0.8.

| Workload | Training examples | Controlled outcome |
|---|---|---|
| Short arithmetic | Short requests to calculate an integer result | Both mock targets pass |
| Long code-review context | Repeated synthetic module/invariant text over 8,000 characters | `cheap` fails; `strong` passes |

The demo injects a synthetic exact-verifier test double; these labels are
controlled wiring evidence, not proof that an actual model solves arithmetic or
code review. Actual isolated verifier execution is tested separately. All 800
requests are fanned out to both targets, yielding 1,600 response and judgment
rows. Request content and raw model responses are unnecessary in the public
summary.

## Training log and fitted models

The recorded fresh pipeline run used seed 42, one training thread, LightGBM 4.7.0
and NumPy 2.5.3. It split conversations into 569 training, 118 validation and 113
test requests. Each target received the same partitions. The measured training
stage took 1.796 seconds on this development run, not a throughput guarantee.

Captured scalar log excerpt from `public-training.log`:

```json
{"event":"training_completed","source":"synthetic","seconds":1.7958723199553788,"seed":42,"threads":1,"split_counts":{"train":569,"valid":118,"test":113}}
{"event":"target_calibrated","source":"synthetic","provider":"synthetic","model":"cheap","n_train":569,"n_valid":118,"calibration_brier":1.357162209529445e-37,"calibration_brier_raw":1.2340164302464178e-11,"mean_out_tokens":55.71177504393673}
{"event":"target_calibrated","source":"synthetic","provider":"synthetic","model":"strong","n_train":569,"n_valid":118,"calibration_brier":0.0,"calibration_brier_raw":1.232595164407831e-30,"mean_out_tokens":55.71177504393673}
{"event":"holdout_evaluated","source":"synthetic","gates":{"complete_holdout":true,"cost_vs_anchor":false,"dominates_bt":true,"floor_violations":true,"quality_vs_anchor":true,"real_data_and_embedding":false,"target_auc_and_coverage":true},"promotable":false}
```

Whitespace is compacted; values are unchanged. This is a projection of actual
completed training, not an invented per-iteration trace. The CLI intentionally
does not print content-bearing rows. An independent `lrp train` rerun on the
same recorded inputs also completed successfully and produced the same bundle
version, `f10b14c5897a571372136168`.

Each target has a quality classifier and a log-output-token regressor. Validation
calibrates the quality score; serving uses the calibrated score. The near-zero
validation Brier values reflect this intentionally easy controlled dataset,
not a generalization claim for production traffic. The bundle binds model files,
embedding representation, feature order and target identities.

## Holdout result and the failed cost gate

| Policy | Mean quality | Floor violation rate | Stored synthetic cost, 113 requests |
|---|---:|---:|---:|
| Learned | 1.000000 | 0.000000 | $0.1507895 |
| Always cheapest | 0.486726 | 0.513274 | $0.0151829 |
| Always anchor | 1.000000 | 0.000000 | $0.1518290 |
| Weighted random | 0.716814 | 0.283186 | $0.0766970 |
| Strength-only/BT | 1.000000 | 0.000000 | $0.1518290 |
| Outcome oracle | 1.000000 | 0.000000 | $0.1507895 |

The learned policy matches the oracle in this controlled dataset, but saves
only **0.68465%** of total cost versus the anchor. Long requests dominate tokens
and spend, so routing short requests cheaply barely changes total cost. The
required cost ratio is at most 0.60 of anchor; the observed ratio is about
0.99315, so `cost_vs_anchor` is false. The synthetic representation also makes
`real_data_and_embedding` false. The bundle is **not promotable**.

All 113 selected outcomes and costs are observed; upstream-reported billed
cost coverage is zero. These costs use stored synthetic usage and rates and
must not be described as provider invoices. The floor sweep does not bypass
either failed gate. This demonstrates why workload mix and token distribution
matter as much as the count of cheap selections.

## Actual inference: target, prediction and reason

The harness starts a normal Go router binary with a temporary test-only signed
license, the LRP HTTP service loading the trained bundle, and loopback mock
upstreams. An observer forwards the router's normalized policy request unchanged
to `/route`, then calls authenticated `/explain` with that exact in-memory
payload. It never saves the payload. The explanation is a separate call with the
same bundle/config, no exploration and no pins, and must agree with the routing
decision. SQLite successful-attempt readback establishes which upstream served.

| Request | Candidate | Predicted quality | Predicted output tokens | Estimated cost | Served? |
|---|---|---:|---:|---:|---|
| Short, 11 estimated input tokens | `synthetic/cheap` | 1.0 | 10.0 | $0.0000021 | Yes |
| Short, 11 estimated input tokens | `synthetic/strong` | 1.0 | 10.0 | $0.0000210 | No |
| Long, 2,535 estimated input tokens | `synthetic/cheap` | approximately 0 | 100.0 | $0.0002635 | No |
| Long, 2,535 estimated input tokens | `synthetic/strong` | 1.0 | 100.0 | $0.0026350 | Yes |

For the short case both targets meet the 0.8 floor, and `cheap` costs less. For
the long case `cheap` remains less expensive but fails the predicted-quality
floor, so `strong` serves. Both recorded labels are
`lrp:cheapest-above-floor`, with policy outcome `selected` and HTTP 200 from
both policy and explanation endpoints. Request IDs in the JSON are freshly
generated synthetic-run router request IDs, not caller/token identifiers.

The cheap quality model's largest local raw-score contribution was `emb_029`:
approximately +12.760 for the short request and −12.362 for the long request.
This is a synthetic hashed embedding dimension, not a named semantic concept.
Contributions explain the fitted model's raw score and are not calibrated
probability increments, a proof of task understanding, or a guarantee of outcome.
For the all-passing strong target, feature contributions are zero in this run.

The separate native-shadow test used a long request: LRP recommended `strong`,
the router served configured-first `cheap`, and telemetry recorded
`shadow_recommended` with the strong candidate signal. This distinguishes a
recommendation from an actual upstream call. Fallback and pinning cases are
also checked independently.

## Test evidence

The attached inference JSON records **11 passing checks**: cheap/strong workload
selection, policy outage fail-closed, policy outage configured fallback, invalid
index rejection, missing-content degradation, upstream-500 fallback, session
pinning, native shadow, latency, and scalar-only/redacted decision persistence.

The direct policy measurement used 500 fresh requests, alternating short and
long synthetic workloads. Every measured request had a fresh selected-policy
row and ordinary learned label; cache/BT fallback could not make the benchmark
appear fast. The recorded router-observed policy p99 was **23 ms**, with child
affinity to four CPUs. Earlier independent runs were **12 ms** and **28 ms**. Observer and
explanation calls are separate and excluded from this measurement. All three runs
use synthetic embeddings and mock upstreams, not real-ONNX performance evidence.

The local XML result was independently reduced to 79 Python tests with actual
verifier execution and zero failures/errors/skips, without copying raw XML or
test output. The implementation lead also recorded 526 Go tests, a successful
docs build, and nonroot container CLI/bundle validation. Those are local integration results; the PR's
current hosted checks and downloadable safe artifacts establish CI status.
Do not infer a passing hosted run from these local counts.

## Real BGE benchmark

This separate benchmark used the official BAAI/bge-small-en-v1.5 artifacts pinned
in the JSON source record, locally dynamically quantized to QInt8 with ONNX
Runtime 1.29.0. The JSON includes source/tokenizer/int8 hashes. It measured real
embedding and full FeatureBuilder computation, including language detection.
It did **not** measure HTTP/router latency, learned quality-model overhead or
provider-backed task outcomes.

Hardware was AMD Ryzen 7 5825U, x86-64, process affinity `[0, 2, 4, 6]`, with
benchmark-request concurrency one, 50 warmups and 500 measured calls per stage
and scenario. **This was a shared development host with concurrent synthetic
training/tests/harness work.** Other workloads used their normal affinities;
four-core affinity did not provide CPU isolation or reserve capacity. These
measurements cannot separate model cost from interference or predict a
dedicated production baseline.

| Input tokens / request bytes | Threads | Embedding p99 | Full features p99 | Embedding ≤15 ms / features <40 ms |
|---|---:|---:|---:|---|
| 26 / 267 | 1 | 12.654 ms | 15.412 ms | Pass / pass |
| 512 / 7,892 | 1 | 304.524 ms | 312.003 ms | Fail / fail |
| 26 / 267 | 4 | 10.355 ms | 13.084 ms | Pass / pass |
| 512 / 7,892 | 4 | 140.146 ms | 161.322 ms | Fail / fail |

Default `serve` loads one inference thread. At 512 tokens, one-thread median
embedding/full-feature latency was 244.405/247.355 ms, already above the 200 ms
service deadline. Expect frequent BT deadline fallback for this tested shape;
timed-out inference occupies its bounded slot until completion, and subsequent
requests can also fall back while workers are busy.

The service default remains 200 ms, with configurable `deadline_ms` from 1 to
4,500 ms for explicitly latency-tolerant workloads or controlled tests. A larger
allowance must be paired with a higher router policy timeout, up to the router's
5,000 ms maximum. It does not change these measured 15/40 ms stage failures or
establish caller latency under load; see the
[deadline configuration guidance](LEARNED_ROUTING_POLICY.md#choose-a-deadline-for-the-workload-or-test).

Four threads are comparative only, not the service default. The long-input
four-thread full-feature maximum was 197.707 ms, leaving little overhead room
within 200 ms. Neither this maximum nor its sub-200 ms p99 validates service or
router deadlines under load. All four scenarios completed; neither long-input
configuration met the 15/40 ms stage budgets. Do not silently reduce 512-token
truncation or change representation: version the features, retrain and validate
the intended request shapes and hardware.

## What remains before promotion

Provider/model/account/dialect smokes, approved real workload collection and
judging, at least 2,000 representative operator-owned requests with disjoint
holdout, provider-backed cost/quality gates, real HTTP/router latency for the
intended shapes and concurrency, and 24-hour staging shadow remain independent
requirements. Preserve failed gates and long-input limitations in any rollout
decision. This example shows a working learned-routing pipeline and inspectable
decisions; it does not supply the missing production evidence.

## Public seed corpus evidence boundary (#157)

Iteration-1 seed tooling constructs teacher-forced turn replays from pinned
public coding trajectories. Until paid replay, verifier labeling on real
candidates, and a published SHA-256 manifest exist, treat seed helpers as
construction wiring only.

Do not cite seed row counts as statistical confidence. Do not equate verifier
tool agreement with task success. Do not promote a first-run reference bundle as
production readiness without operator workload validation. Cache-pass duplicates
are not independent samples. Savings percentages are out of scope for seed
evidence tables.

