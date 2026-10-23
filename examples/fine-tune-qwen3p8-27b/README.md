<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Fine-tune Qwen3.8-27B and govern it with Metrum AI Router

Work in progress for #326. Do not infer training completion or promotion from
this runbook. Actual results live in `docs/evidence/finetune/2026-10-23/`.

## Mandatory authenticated preflight

Before each batch, launchers probe every configured caller/route using only a
synthetic `PREFLIGHT_ONLY` prompt. The example's weighted/shadow profiles also
probe each static target alias; semantic runs probe the rubric grader **before**
starting promptfoo. Any non-2xx, including pre-route400/401/403/429, aborts the
whole run immediately. No retries, dataset prompts, gold or answer text enter
preflight receipts. Probes are charged/warm-up traffic, not scored cases or
latency samples; full collection includes them in its dollar reservation ledger.

For custom weighted profiles pass **every** static target alias through the
launcher's `--preflight-targets` option. Grant those aliases to the same caller
key, with high rpm/tpm/quotas/concurrency. Unreachable/unauthorized aliases are
not replaced with privileged keys. External replay launchers can use the same
standalone gate once per caller/token file before starting any batch:

```sh
python examples/fine-tune-qwen3p8-27b/preflight.py \
  --api-base http://127.0.0.1:18525/v1 --token-file /secure/router.token \
  --models qwen3p8-27b-base qwen3p8-27b-ft frontier-ref \
  --out /secure/preflight.json
```

Only separately labelled `--test-rate-limiting` smoke bypasses normal preflight
because its purpose is deliberate denial. An unexpected pre-route4xx requires
a documented infra-only amendment and a fresh **whole-run** attempt, never
selectively repaired cases. Frozen #358 dataset files remain immutable.

## AMD Instinct

The [AMD runbook](amd/README.md) records one epoch on the governed router-exported
1186/221/300 split via Transformers/PEFT: pinned Axolotl dependency resolution
failed against the official ROCm Torch build. Serving used a safe **BF16 merge**
because ROCm vLLM unmerged-LoRA loading failed. Primary JSON-text exact was
**3.5% vs base4.5% (−1.0pp; paired95%CI[−2.5,0]pp): gate FAIL, not promoted**.
The droplet was destroyed and deletion API-verified by the orchestrator at19:23Z.
See the [final AMD evidence](../../docs/evidence/finetune-amd/2026-10-23/README.md).
This is not evidence of NVIDIA parity or healthy-teacher savings. It uses #358's
router export, not the direct-download dataset below.

The initial26local concurrency429s were **self-inflicted infrastructure
throttling**, not model failures. The entire initial load attempt is excluded
from all reported benchmark denominators; all three load profiles were rerun
whole at high capacity before qualification. Historical failures/costs remain
in the lifecycle ledger only. Per #370, all local/test caller limits are now
high (including grader keys and effective project/group profiles); only clearly
labelled intentional limit tests may throttle.

The maintainer authorized the Fireworks-hosted open-weight teacher for the
synthetic Acme/mock-tool technical test (#358), not general customer traffic.
Record `teacher_terms_decision` in the export manifest and evidence. Users must
choose a teacher whose model license and provider terms permit training on
outputs, and verify those rights before enabling export for training.

## 1. Industry problem: training is not safe production

Fine-tunes rarely reach production safely: a public training corpus is not the
customer's work; ungoverned agent traffic lacks consent and outcome labels;
there may be no safe route back to the originating callers; evaluation may not
match production tool continuations. Token price alone misses the cost of
retries and failed tasks. Measure **cost per successful task**, retaining failures
in the denominator, alongside quality, regressions and latency.

## 2. Router-centric solution

Serve the teacher → enable governed capture for one key/project only → attach
outcome labels → audited canonical JSONL/manifest export with frozen time splits
→ fine-tune externally → register the target in the **same `support-agent` group**
with a key-scoped canary → replay router-captured fixtures through the router →
let the preregistered gate decide promotion or rollback. See
[`router-loop/README.md`](router-loop/README.md) for the two-key Acme harness,
admin API, privacy/manifest contract and caller-bound script. Choose a teacher
whose **license and provider terms permit training on outputs** and verify that
before export. Our scoped synthetic technical-test decision is not customer-data
permission. Public seed answers must not bypass governed capture into training.

## 3. What we did: run 1 was rejected

Run 1 used direct public data, not customer/router-captured training data. Its
weighted candidate improved a narrow strict JSON-text extraction metric from
4.5% to 32.0%, while wrapper validity fell from 99.5% to 42.5%. General regression
checks failed: IFEval 81.5% → 63.5%; MMLU-Pro 60.5% → 13.0%. The gate rejected it;
**base remains default**. These are formatting-sensitive results, not native
API tool-selection quality or production ROI. See the evidence
[`RESULTS.md`](../../docs/evidence/finetune/2026-10-23/RESULTS.md) for denominators,
confidence intervals, weak judge calibration and hardware/cost limitations.
Lesson: successful training, faster serving and one improved metric do not
justify promotion; data/task mismatch and general regressions matter.

## 4. Historical engineering snapshot — before the final governed run

This 2026-10-23 small-sample snapshot and its pending-work list are historical,
not the current AMD status. The AMD outcome and verified teardown are above;
its final governed split is1186/221/300, not this seven-row engineering sample.
The historical caller-budget429 below is an infrastructure denial, not a model
failure or part of any reported AMD benchmark denominator.

The router-loop engineering sample used a real Fireworks open-weight teacher
under two keys: nine public task seeds per key, eight tool-result continuations
and seven deterministic task successes per key. Capture enabled only for support;
audited success-only export produced 2 train / 2 validation / 3 test rows, no IT
rows. Same-group key+project routing was demonstrated with a **synthetic candidate
stand-in**, not a trained fine-tune; a second support request hit its caller
budget (429). Small-sample success is not a quality gate. At the observed public
list prices and router token counts, each nine-task caller cohort cost an
estimated $0.00210315, or $0.00030045 per successful task **including failed-task
tokens**. These tiny-sample list-token estimates ignore cache discounts and are
not invoices, hourly serving economics or production ROI. See
[`router-loop-sample.json`](../../docs/evidence/finetune/2026-10-23/router-loop-sample.json).

Still pending: useful-sized governed training cohort, trained-target canary,
frozen held-out candidate/teacher/base replay, comparable latency distributions
and measured production cost per successful task. No positive run-2 outcome or
promotion is implied. The run-1 reproduction instructions below remain historical
and are not a direct-download training fallback for the router-centric run 2.

## Pins and rights

- Base: `Qwen/Qwen3.8-27B@1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0`,
  Apache-2.0 (verified Hub LICENSE), public and ungated; see MODEL_LICENSES.md.
- Training: Glaive AI `glaiveai/glaive-function-calling-v2` revision
  `e7f4b6456019f5d8bcb991ef0dd67d8ff23221ac`, Apache-2.0, ungated.
  Retain upstream attribution/NOTICE and record modifications. No real rows in Git.
- Judge calibration: LMSYS `lmsys/mt_bench_human_judgments` revision
  `f7d2896d2cc5d80f8b55c2bbc722613555233c25`, CC-BY-4.0 per pinned dataset card.
  Attribute Zheng et al., *Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena*
  (2023), arXiv:2306.05685. Use the **human** split, not `gpt4_pair`.
  Public human labels satisfy the maintainer's calibration decision; do not
  relabel AI-generated judgments as human. Pairwise agreement on this public
  corpus is not a domain-specific correctness guarantee.

## Prepare and test without spending

Install CPU-only data/eval tools with `python3 -m pip install -r eval/requirements.txt`
in a private venv. Do not install the GPU training stack or load weights locally.
Download the pinned Glaive JSON outside Git
using `huggingface_hub` (read `HF_TOKEN` from the environment, never argv).

```sh
python3 prepare_data.py --input /secure/glaive.json --out /secure/canonical --train-size 2000
python3 -m unittest -v
python3 standalone/cloud.py vast offers
python3 standalone/cloud.py shadeform offers
```

The first economical run uses 2,000 training rows, 500 validation rows and 300
held-out test rows. Canonical chat JSONL hashes are recorded in `manifest.json`.
Normalization is NFKC + casefold + whitespace folding; reject test leakage.
Only first turns with valid declared tools are retained. Safe literal parsing
handles Glaive's mixed Python/JSON quoting without executing source text. Seed326
stratification fixes the tool/text ratio at2:1: train1333/2000, val333/500,
test200/300 tool targets. Missing either class fails before spending. Canonical
structure is schema-validated; references are synthetic and not independently
human-verified. This conversion is not the upstream's full benchmark.

## Train and serve

Fireworks reports `tunable=false`, `supportsLora=true`: use the issue's fallback
**standalone train → upload LoRA → dedicated Fireworks deployment**. Do not claim
independent managed training. Both serving stacks use the same adapter.

Provision only once the dataset, pinned environment and commands are ready.
Use Shadeform first and also check Vast for B200s. Approved GPUs are RTX PRO
6000 Blackwell, H100, H200 and B200, up to eight GPUs. Pick the cheapest
configuration that actually fits; verify with the tiny hardware smoke before
training. Require CUDA ≥12.8, ≥500 GB disk and ≥1 Gbps network. Report advertised
network separately from measurements.
`standalone/cloud.py` supports both marketplaces; mutations default to dry-run.
Use a fresh per-instance SSH key, attach only its public key, keep runtime state
outside Git, and verify deletion through the API even after failures.

Primary toolkit: **Axolotl**, whose upstream includes an explicit Qwen3.8-27B
recipe (inspected at commit `777990d36ab92dcdc8b62d5ad07dc30b9618ff25`).
`standalone/axolotl.yaml` is BF16 LoRA, not QLoRA. `standalone/fsdp2.yaml` is
merged by `standalone/render.py` for 2/4/8 GPUs, keeping effective batch 16.
The launcher pins the official Axolotl image by digest and saves its actual
package inventory in `requirements.lock`. This inventory includes the image's
Axolotl 0.21.1.dev0 and is not a pip-installable PyPI lock. Use the pinned image
to reproduce it. `requirements.in` names the released 0.21.0 alternative for
operators, but that alternative is not claimed to be hardware-tested.
If the real hardware smoke shows unsupported architecture, use Unsloth only
after verifying its exact Qwen3.8 recipe/version; record why, never silently
change quantization or training parameters.

Download the exact base revision on the remote host. Pass `HF_TOKEN` over
encrypted stdin into the single `standalone/download_model.py` process; do not
persist tokens or copy credentials. Mount host `/workspace` at container `/data`:
mounting over the image's `/workspace` would hide Axolotl's bundled venv.

```sh
# On the GPU host after copying this example to /workspace/standalone:
DRY_RUN=1 bash /workspace/standalone/run.sh smoke
DRY_RUN=0 GPUS=1 bash /workspace/standalone/run.sh smoke
# Only after the 2-step, 512-token smoke succeeds:
DRY_RUN=0 GPUS=1 bash /workspace/standalone/run.sh train
MODEL_DIR=/workspace/base ADAPTER_DIR=/workspace/adapter DRY_RUN=1 bash serve/run.sh
# After reviewing, DRY_RUN=0 launches the pinned official vLLM image.
```

Training uses text-only BF16 LoRA, rank 16, alpha 32, two epochs, learning rate
1e-4, cosine decay, effective batch 16, max sequence 8192, gradient checkpointing.
The vision tower and LM head remain frozen and are checked before training.
The byte-identical canonical training file is the upload input if managed SFT
becomes available later; token rendering is a trainer-only transformation.

Reach vLLM from gyrados using `ssh -N -L 127.0.0.1:18526:127.0.0.1:8000 ...`.
Never publish an unauthenticated model endpoint. The Docker recipe publishes
only loopback on the host. Native serving inside an existing GPU container must
also use `--host 127.0.0.1`.

## Fireworks adapter upload and dedicated deployment

Run these locally on gyrados, **not on the rented GPU**. Copy only the adapter
artifacts back over SSH, compare SHA256 hashes, and retain them outside Git.
Read the API key from `FIREWORKS_API_KEY`; never copy it to a GPU host.

```sh
# All mutations are dry-run unless --execute is supplied. Account ID is not a key.
python3 fireworks/run.py upload --account "$FIREWORKS_ACCOUNT_ID" --adapter /secure/adapter
python3 fireworks/run.py upload --account "$FIREWORKS_ACCOUNT_ID" --adapter /secure/adapter --execute
# Inspect model state READY and the default shape/price before paying for replicas.
python3 fireworks/run.py deploy --account "$FIREWORKS_ACCOUNT_ID"
python3 fireworks/run.py deploy --account "$FIREWORKS_ACCOUNT_ID" --execute
python3 fireworks/run.py status --account "$FIREWORKS_ACCOUNT_ID" --execute
# At step end, delete your OWN disposable deployment promptly. Recent inference
# otherwise blocks deletion for an hour. This explicit flag bypasses that guard;
# never apply it to somebody else's/production deployment. Poll exact-ID state.
python3 fireworks/run.py delete --account "$FIREWORKS_ACCOUNT_ID" --ignore-recent-inference --execute
```

Default shape is **not necessarily the cheapest hardware**: our first attempt
resolved to2×B300 at the public $15/GPU-hour quote, $30/hour for one replica.
It was stopped while still CREATING when dataset QA invalidated that adapter.
Set an independent bounded cleanup timer; min/max replicas1 still bills while
idle. Use `--resume-upload` only for your own existing same-base adapter after
an interrupted upload. Signed GCS URLs stay in memory and require both
`Content-Type: application/octet-stream` and exact-size
`x-goog-content-length-range: N,N`; never print those URLs. The model selector in
the router template explicitly names the dedicated deployment with `#`, avoiding
an accidental shared/default-deployment route.

## Router groups, caller budgets and rollback

Copy `config.qwen3p8-ft.example.yaml` outside Git and replace the Fireworks account
and deployment endpoint as appropriate. `vllm_b200_ft` is a logical provider name
retained from the issue, not a claim that the current host is a B200: this run
uses RTX PRO 6000 Blackwell. The default FT group splits traffic 50/50. The static
`-onprem` and `-fireworks` FT groups isolate serving stacks for paired evaluation.
The default route stays on the base. Optional `qwen3p8-27b-canary` starts at
90% base/5% on-prem FT/5% Fireworks FT. The `-shadow` group uses
`python3 eval/shadow_policy.py`: base serves while an FT recommendation is
recorded with decision telemetry. This is recommendation-only shadowing, not a
second model inference. Never switch production traffic based on smoke alone.

Generate a fresh narrow caller (never pass a raw key on argv):

```sh
go run ./cmd/metrum-ai-routerctl callers generate \
  --owner-user local-dev --project example-project --env dev \
  --allow qwen3p8-27b-ft,qwen3p8-27b-base,qwen3p8-27b-ft-onprem,qwen3p8-27b-ft-fireworks,qwen3p8-27b-canary,qwen3p8-27b-ft-shadow,frontier-ref \
  --token-out /secure/router.token --config /secure/ft.yaml --write
```

Before starting any localhost/test workflow, set **every** generated caller to
high test capacity (not production defaults or permission to spend):

```yaml
rate: {rpm: 100000, tpm: 100000000, concurrent: 256}
quota:
  day: {requests: 10000000, tokens: 2000000000}
  month: {requests: 100000000, tokens: 20000000000}
```

`scripts/local_dev_bootstrap.py` applies these local-only defaults automatically;
the generic caller-generation CLI retains its production-oriented defaults, so
apply the block above when using that CLI directly. Project identities and static
model groups do not have independent rate/quota fields in this config schema;
configure every key associated with each project/group. Script canaries set
`script_max_concurrent: 256`. Load/replay/judge keys need the same headroom.
Normal mock e2e uses high limits. Only `eval/router_e2e.py --test-rate-limiting`
intentionally installs a tiny quota and labels its result `rate-limiting-test`.
Any unexpected pre-route429 invalidates the entire run: document an infra-only
amendment and rerun the whole run, never selected cases. Upstream provider429s
must be reported separately and never silently retried or reclassified.
`spend_ceiling.max_estimated_cost_usd` is a **per-request** estimated-cost ceiling,
not an hourly GPU spending cap. It is a dynamic-strategy eligibility control;
weighted/external strategies do not enforce it. The actual AMD external-policy
control returned200 under a tiny ceiling: do not claim global budget enforcement.
Zero list-token prices in this template cannot
protect a rental budget; use the separate hard GPU lease watchdog and track real
hourly charges. Replace placeholder amortized prices before cost-based routing.

Rollback: remove the candidate target (or set its route weight to zero where
supported) and route callers to the base group. Disable content capture; do not
publish promptfoo raw outputs. `eval/run_eval.py` keeps raw configs/results in a
private output location and reads the caller key only from its token file into
the child environment. `eval/summarize.py` publishes scalars and paired CIs.

For a **synthetic local-dev** rollout proof (not promotion), start the loopback
shadow policy and run the bounded routing/DB/privacy check:

```sh
(
  python3 eval/shadow_policy.py & policy=$!
  trap 'kill "$policy" 2>/dev/null || true' EXIT
  python3 eval/governance.py --token-file /secure/router.token \
    --usage-db /secure/router-usage.sqlite --router-log /secure/router.log \
    --out /secure/governance-scalars.json
)
```

## Evaluate through the localhost router

From this example directory, after both serving stacks are READY and the router
is running. Keep all generated test prompts and raw model results outside Git.
The runner uses four arms: base, weighted FT group, isolated on-prem FT and
isolated Fireworks FT. A/B routing is request-weighted, not token-weighted.

```sh
python3 eval/live_smoke.py --token-file /secure/router.token \
  --model qwen3p8-27b-ft-onprem --out /secure/live-onprem.json
python3 eval/live_smoke.py --token-file /secure/router.token \
  --model qwen3p8-27b-ft-fireworks --out /secure/live-fireworks.json
python3 eval/build_tests.py --test /secure/canonical/test.jsonl --out /secure/held-out.yaml
python3 eval/run_eval.py --tests /secure/held-out.yaml --token-file /secure/router.token \
  --out /secure/quality.json
python3 eval/summarize.py --input /secure/quality.json --journal /secure/quality.router-ids.jsonl \
  --usage-db /secure/router-usage.sqlite --aggregate-joins --out /secure/quality-scalars.json
python3 eval/prepare_regressions.py --out /secure/regressions
python3 -c "import nltk; nltk.download('punkt_tab')"
export IFEVAL_VERIFIER_ROOT=/secure/regressions
python3 eval/run_eval.py --tests /secure/regressions/tests.yaml --token-file /secure/router.token \
  --out /secure/regressions.json
python3 eval/summarize.py --input /secure/regressions.json --case-prefix ifeval \
  --journal /secure/regressions.router-ids.jsonl --usage-db /secure/router-usage.sqlite \
  --aggregate-joins --out /secure/ifeval-scalars.json
python3 eval/summarize.py --input /secure/regressions.json --case-prefix mmlu-pro \
  --journal /secure/regressions.router-ids.jsonl --usage-db /secure/router-usage.sqlite \
  --aggregate-joins --out /secure/mmlu-scalars.json
for seed in 326 327 328; do
  python3 eval/run_eval.py --tests /secure/held-out.yaml --non-tool-only \
    --temperature .7 --seed "$seed" --token-file /secure/router.token \
    --out "/secure/sampling-$seed.json"
  python3 eval/summarize.py --input "/secure/sampling-$seed.json" \
    --journal "/secure/sampling-$seed.router-ids.jsonl" --usage-db /secure/router-usage.sqlite \
    --aggregate-joins --out "/secure/sampling-$seed-scalars.json"
done
python3 eval/outcome.py --quality /secure/quality-scalars.json \
  --ifeval /secure/ifeval-scalars.json --mmlu /secure/mmlu-scalars.json \
  --base openai:chat:qwen3p8-27b-base --candidate openai:chat:qwen3p8-27b-ft \
  --out /secure/outcome.json
```

Promptfoo exit100 means assertion failures; still summarize the completed run and
verify full denominators/joins. It is not a promotion pass or proof every request
completed. Other exit codes/incomplete joins need investigation, not exclusion.

The regression preparer fetches the pinned licensed verifier source automatically;
use the private CPU venv above and `IFEVAL_VERIFIER_ROOT` when running it. Do not
substitute a permissive IFEval string matcher.
Exact match/F1 measure **structured tool-selection/JSON extraction**, while the
native `tools` API and file/tool-result continuation are a separate live smoke.
The frozen `refusal` field marks parseable responses with no tool calls when a
tool was expected. Malformed outputs score0 in that field and are exposed by
`valid=0` separately; it is not a comprehensive refusal detector. `json_schema`
checks the outer tool-call wrapper, not each function's parameter schema.
Do not call this a human-coded safety-refusal rate. Summarize the three sampling
runs separately (paired case bootstrap per run); repeated answers to the same
case are not independent human judgments.

Promptfoo deliberately redacts HTTP headers. `telemetry.cjs` allowlists only
safe request IDs, provider group and case index into a mode0600 private journal
before redaction; it never journals whole headers or prompts. Missing/duplicate
IDs or incomplete DB joins fail closed. `--aggregate-joins` additionally rejects
reused request IDs and emits scalar counts/target counts/statuses/token totals and
hashes, not per-request rows; provider account IDs are redacted. Keep the full
joined rows/journal private. Numerical provider/paired metrics are unchanged.
Without that flag the summary includes IDs/allowlisted usage rows; never publish
raw model exports. Review summaries before publishing.

For serving measurements use `eval/load.py --help`: SSE concurrency1/8/32,
explicit hourly rate, ≥32 requests per scenario, TTFT/TPOT/end-to-end latency,
throughput, peak GPU memory (sample separately), and hourly cost per1M outputs.
Model errors or missing streaming usage make the load command fail. A merged
variant is supported by `standalone/merge.py`, then `MODE=merged` with
`serve/run.sh`; do not change quantization, dataset or outcome thresholds while
comparing merged/unmerged serving. Never load/merge weights on gyrados.

`eval/economics.py` converts an error-free measured load profile into an explicit
capacity/duty-cycle break-even **scenario**. For example, the pinned judge's published
$2.50/$10 input/output list rates can be a token-priced reference, but this is
**not** a demonstrated quality-equivalent alternative or business payback:

```sh
python3 eval/economics.py --load /secure/load-onprem.json --concurrency 32 \
  --reference-input-usd 2.5 --reference-output-usd 10 --input-output-ratio 1 \
  --hours 720 --one-off-usd 0 --out /secure/economics-scenario.json
```

State the assumed input/output ratio, utilization, period and any one-off
training cost. Throughput applies only to the measured short-prompt,
128-token-cap synthetic workload. Scaling its efficiency down to an average
load fraction assumes batching/bursts; continuous low-concurrency traffic or a
changed input/output mix has not been validated. Hourly bills continue when idle; operator
labor, storage, network, taxes, retries and quality differences are not measured.
Against the same rented base model, fine-tuning does not magically remove GPU
hourly cost; no reduced-retry savings or positive ROI may be inferred.

## Budget and release safety

Latest maintainer decision: **$1,000 on-prem GPU allowance, stop at $800 GPU**,
separate from Fireworks. Keep Fireworks modest (original allowance $300; stop
and report before $240). Track OpenAI judge costs separately too.
Record every paid step and wall-clock rental cost in the track report. Dedicated
Fireworks deployments bill while idle. Delete deployments and instances at step
end and verify via APIs; remove per-instance SSH keys. Full SFT on 8× B200 needs
explicit maintainer approval and is not authorized by this example.

Quality and tool-continuation evidence must use a localhost router built from
the worktree. Promotion requires preregistration before the first eval, +10 point
tool exact match improvement, ≤2 point regression, no higher error rate and p95
latency ratio ≤1.25. Report means, paired comparisons and 95% bootstrap CIs.
No results or gate pass are implied until those checks actually run.
