<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Repeatable LRP example: train and deploy a coding mix on Metrum AI Router

**Audience:** platform engineers who will copy this into their own environment.  
**Product:** Metrum AI Router + Learned Routing Policy (LRP)  
**Date:** 2026-09-14  
**Stand-alone file:** `docs/share/lrp-coding-mix-example.md` (Markdown only; no PDF)

This is a **repeatable worked example**, not a marketing one-pager. It documents
the dataset, CLI train path, router wiring, measured absolute USD / latency, and
how to swap in your own targets (including a priced **GPT-6 Astra** slot that was
**not** executed in this run).

Repo: https://github.com/metrum-ai/router

---

## 1. Problem statement

A single “coding” model group that always hits a frontier model is simple and
expensive. Static weights are cheaper to operate but do not learn from your
outcomes. The operational question is:

> For each request, which eligible upstream is the **cheapest target predicted
> to clear our quality floor**?

That answer must come from **multi-target outcomes on your workload shape**,
joined to request-time prices, then served beside the router. List prices alone
do not establish sufficiency.

## 2. Why LRP (and what it is not)

| LRP does | LRP does not |
| --- | --- |
| Train per-target quality + output-token models | Fine-tune the upstream LLMs |
| Recommend among **already eligible** router targets | Bypass access control, PII, or eligibility |
| Estimate cost from rates × predicted tokens | Invent free targets when prices are missing |
| Serve a versioned, hash-checked bundle | Run judges or training on the hot path |

Metrum AI Router still owns the upstream call, usage, latency, and fallback.
LRP is an `external` routing policy (`POST /route`) that returns payload-local
indexes.

## 3. Architecture (train vs serve)

```
                    OFFLINE (private LRP_DATA_DIR)
  dataset / capture
       -> collect (requests.ndjson)
       -> fanout via router (responses.ndjson)   [paid if live]
       -> judge / verifiers (judgments.ndjson)
       -> featurize (features.parquet)
       -> train (bundles/<version>/)
       -> eval (eval_report.json)
       -> validate / optional sign-bundle

                    ONLINE
  caller -> Metrum AI Router (strategy: external)
              -> lrp serve :18093/route
              -> selected upstream
              -> usage + decision telemetry
```

Feature vector: embedding (default ONNX int8 BGE-small) + scalars (token
estimates, tool counts, etc.). Train and serve share the same normalization
and `max_seq_len` (default 512). Session-hash splits keep sessions in one
partition only. Targets with fewer than **200 training rows** are excluded from
learned selection.

---

## 4. Dataset used in this example

### 4.1 Identity (pinned)

| Field | Value |
| --- | --- |
| Hugging Face dataset | `nebius/SWE-rebench-openhands-trajectories` |
| Git / dataset revision | `35455389ab51bf5e2306bfd436ef72d0f98bf882` |
| Constants | `services/learned-routing-policy/lrp/seed/__init__.py` |
| License audit date | 2026-09-12 (`AUDIT_DATE`) |

### 4.2 Construction semantics

Seed helpers live under `services/learned-routing-policy/lrp/seed/`:

| Module | Role |
| --- | --- |
| `download.py` / `license_audit.py` | Pin and audit source |
| `extract.py` | Stream parquet; teacher-forced turn extraction (no full `to_pandas()`) |
| `sample.py` | Stratified sample |
| `targets.py` | Iteration-1 target descriptors + dated prices |
| `replay.py` | Spend estimate + optional paid fanout wrapper |
| `labels.py` | Verifier column helpers |
| `parquet_manifest.py` | SHA-256 manifest writer |

**Teacher-forced continuation:** each turn is a fixed history ending at the last
tool or user observation. A plausible next assistant action is **not** proof of
eventual task success. Later history is unchanged by candidate outputs.

### 4.3 Example budget (what we actually sampled)

| Knob | Value | Meaning |
| --- | --- | --- |
| `EXAMPLE_MAX_TRACES` | 100 | Max source traces streamed |
| `SAMPLE_N` | 100 | Max stratified turns kept |
| `DEFAULT_SAMPLE_SEED` | 42 | Reproducible sample |
| Extracted turns before sample | 6605 | From 100 traces |
| Split after sample | train 76 / valid 19 / test 5 | Session-disjoint |
| Prompt tokens (cl100k metadata) | mean 21377 / min 3954 / max 83535 | Estimate input |
| `SPEND_ABORT_USD` | 100.0 | Fanout abort without approval |

This is an **example budget**, not statistical sufficiency for production.

### 4.4 Apply to your own data

Replace the public trajectory source with your approved capture:

1. Export requests with content you are authorized to process.
2. Keep stable `request_id` / `session_key`, dialect, tools, and caps.
3. Prefer the same client shapes you serve in production.
4. Do **not** reconstruct prompts from metadata-only router logs.

---

## 5. Portfolio: what ran vs what you can add

### 5.1 Executed in this example (paid fanout on Shadeform)

| target_id | provider | model | list prices used (per 1M tok) |
| --- | --- | --- | --- |
| openrouter-qwen3.5-9b | openrouter | `qwen/qwen3.5-9b` | in 0.10 / out 0.15 |
| openrouter-qwen3.8-27b | openrouter | `qwen/qwen3.8-27b` | in 0.214 / out 2.55 / cached in 0.15 |
| openrouter-minimax-m3 | openrouter | `minimax/minimax-m3` | in 0.30 / out 1.20 |
| fireworks-kimi-k2p7-code | fireworks | `accounts/fireworks/models/kimi-k2p7-code` | in 0.95 / out 4.00 / cached in 0.19 |
| baseten-glm-5-2 | baseten | `zai-org/GLM-5.2` (`model_ref` `glm-5-2`) | in 1.50 / out 4.50 / cached in 0.30 |

Descriptors: `services/learned-routing-policy/lrp/seed/targets.py`.  
Keys used: `OPENROUTER_API_KEY`, `FIREWORKS_API_KEY`, `BASETEN_API_KEY` from
`env.json` (never committed).

**Absolute-USD baseline for this mix:** Baseten GLM-5.2 (`baseten-glm-5-2`).

### 5.2 Optional strong anchor: GPT-6 Astra (not executed here)

You can add OpenAI `gpt-6-astra` as a frontier / absolute-USD baseline for
**your** run. It was **not** called in this example (portfolio pivoted off
OpenAI after rate-limit failures). Prices below are from OpenAI primary docs
checked **2026-09-14**:

Source: https://developers.openai.com/api/docs/models/gpt-6-astra  
Also: https://developers.openai.com/api/docs/pricing

**Standard, short context (input ≤ 272K tokens), per 1M tokens USD:**

| | Input | Cached input | Cache writes | Output |
| --- | ---: | ---: | ---: | ---: |
| `gpt-6-astra` | 10.00 | 1.00 | 12.50 | 50.00 |

**Long context:** if input **exceeds 272K tokens**, long-context rates apply to
the **entire** request (not only the excess): input/cache ×2, output ×1.5
(so 20 / 2 / 25 / 75 at Standard).

Other notes from the model card:

- Context window 1,050,000; max output 128,000
- Chat Completions and Responses; use `max_completion_tokens` for GPT-5/6-style
  chat caps
- Batch/Flex ≈ 50% of Standard; Fast ≈ 2× Standard
- Function calling / tools supported

Example target descriptor shape (adapt into your private `targets.yaml`; do not
commit secrets):

```yaml
- target_id: openai-gpt-6-astra
  provider: openai
  model: gpt-6-astra
  model_ref: gpt-6-astra
  router_group: lrp-seed-gpt-6-astra
  honors_max_tokens: true
  output_token_field: max_completion_tokens
  router_targets:
    - provider: openai
      model: gpt-6-astra
      model_ref: gpt-6-astra
  context_tokens: 1050000
  common_eligible_context_tokens: 272000
  pricing:
    input_per_m_usd: 10.0
    output_per_m_usd: 50.0
    cached_input_per_m_usd: 1.0
    cache_write_per_m_usd: 12.5
    source: openai_primary_docs
    as_of: "2026-09-14"
  pass1_controls:
    cache: disabled
    # Prefer explicit cache-disable controls when documenting pass-1 economics
```

**Back-of-envelope on this sample’s mean prompt (~21.4k tokens) + 512 out:**

`cost ≈ (21400×10 + 512×50) / 1e6 ≈ 0.240 USD per turn` at short-context
Standard rates (before tools, reasoning, long-context repricing, or cache).
At N=100 that is roughly **24 USD** for an Astra-only baseline slice — far above
the executed GLM slice (~1.58 USD on ok rows). Refresh prices before any paid
estimate; do not treat this paragraph as a live quote.

---

## 6. Environment and host

### 6.1 Orchestration vs heavy ML

- **Orchestration host:** git, `gh`, SSH, secret path wiring only.
- **Train / featurize / large extract:** remote GPU with ample RAM (Shadeform or
  BYO). See `docs/LRP_GPU_TRAINING.md`.
- CI `make lrp-test` may train LightGBM on tiny **synthetic** fixtures on CPU.
  That is not hardware evidence.

This example’s GPU host class: Shadeform `L40Sx2`, 2× L40S. Use an
operator-private work root under `$LRP_DATA_DIR` (not committed). Ephemeral CPU
A/B/C used `cpu_small` then torn down.

### 6.2 Software setup

```bash
# From the router checkout
uv sync --project services/learned-routing-policy --locked

# Alias for the rest of this doc
alias lrp='uv run --project services/learned-routing-policy --locked lrp'

export LRP_DATA_DIR=/var/tmp/lrp-coding-mix   # outside the public tree
mkdir -p "$LRP_DATA_DIR" && chmod 700 "$LRP_DATA_DIR"

# Load keys without printing them (example)
set -a
source <(jq -r 'to_entries[] | "\(.key)=\(.value|@sh)"' /path/to/env.json)
set +a
```

### 6.3 Embedding artifacts + device

Default embedder: local int8 ONNX export of
[BAAI/bge-small-en-v1.5](https://huggingface.co/BAAI/bge-small-en-v1.5) + matching
`tokenizer.json` under `$LRP_DATA_DIR/embed/` (operator-owned; not downloaded at
serve).

On the GPU host, install **`onnxruntime-gpu`** (operator-managed; keep the repo
lockfile on CPU ORT for CI). Then:

```bash
# Prefer explicit device for evidence / promotion
# --device cuda:0 --strict-device
```

Optional: `--embedding-backend sentence-transformers` after
`uv sync --project services/learned-routing-policy --extra embed-st` with a
**local or already-cached** model directory (no serve-time download).

---

## 7. End-to-end CLI (copy/adapt)

All non-synthetic collect / fanout / judge steps require `--approved-content`
after you approve that content transfer.

### 7.1 Router: one group per candidate

Each fanout target needs a **single-target** router group so response rows
identify the upstream that answered. Shape (private config, not committed):

```yaml
# Fragment: model_groups.<id>
lrp-seed-qwen35-9b:
  strategy: static
  targets:
    - provider: openrouter
      model_ref: qwen/qwen3.5-9b
# ... one group per candidate (minimax, kimi, glm, optional gpt-6-astra)
```

Caller token must allow exactly those groups. Start the router with provider
keys in the environment and a local listen address used by fanout
(`http://127.0.0.1:8080/v1` in this example).

### 7.2 Collect requests

From an approved dataset export:

```bash
lrp collect \
  --dataset "$LRP_DATA_DIR/seed.ndjson" \
  --approved-content \
  --out "$LRP_DATA_DIR/requests.ndjson"
```

Or join governed content capture to router metadata (see runbook). Metadata-only
router JSONL **cannot** invent prompts.

### 7.3 Targets file for fanout

Project `iteration1_targets()` (or your edited list) into a private
`targets.yaml` matching `FanoutTarget` fields: `provider`, `model`, `model_ref`,
`honors_max_tokens`, `router_group`, `router_targets`, `output_token_field`.

### 7.4 Spend estimate **before** paid fanout

```bash
# Seed path: estimate_replay_spend_usd() / guard_spend_estimate()
# Abort when estimate_usd > SPEND_ABORT_USD (100) without approval.
```

This example: estimate **7.105 USD**, abort **100**, then execute.

### 7.5 Fanout (paid when `execute=True`)

```bash
lrp fanout \
  --requests "$LRP_DATA_DIR/requests.ndjson" \
  --targets "$LRP_DATA_DIR/targets.yaml" \
  --via router \
  --base-url http://127.0.0.1:8080/v1 \
  --refresh-pricing \
  --approved-content \
  --concurrency 1 \
  --max-total-cost-usd 100 \
  --out "$LRP_DATA_DIR/responses.ndjson"
```

Inspect `upstream_error` / `ineligible` rows before judging. Unknown prices must
not win as free. Honor `output_token_field` (`max_tokens` vs
`max_completion_tokens`).

### 7.6 Labels

Prefer deterministic verifiers in an isolated rootfs:

```bash
# Build/preflight rootfs per services/learned-routing-policy/lrp/judge/README.md
export LRP_VERIFIER_ROOTFS=/path/to/verified-rootfs

lrp judge \
  --requests "$LRP_DATA_DIR/requests.ndjson" \
  --responses "$LRP_DATA_DIR/responses.ndjson" \
  --approved-content \
  --sandbox-rootfs "$LRP_VERIFIER_ROOTFS" \
  --out "$LRP_DATA_DIR/judgments.ndjson"
```

Optional third-party judge (sends content off-box; authorize retention):

```bash
lrp judge \
  --requests "$LRP_DATA_DIR/requests.ndjson" \
  --responses "$LRP_DATA_DIR/responses.ndjson" \
  --anchor-provider openrouter \
  --anchor anthropic/claude-sonnet-4.6 \
  --judge anthropic/claude-sonnet-4.6 \
  --approved-content \
  --sandbox-rootfs "$LRP_VERIFIER_ROOTFS" \
  --out "$LRP_DATA_DIR/judgments.ndjson"
```

Keep verifier columns separate from judge / human labels.

### 7.7 Featurize

```bash
lrp featurize \
  --requests "$LRP_DATA_DIR/requests.ndjson" \
  --embedding-backend onnxruntime \
  --embedding-model "$LRP_DATA_DIR/embed/model.onnx" \
  --tokenizer "$LRP_DATA_DIR/embed/tokenizer.json" \
  --device cuda:0 \
  --strict-device \
  --seed 42 \
  --out "$LRP_DATA_DIR/features.parquet"
```

Optional near-dup filter before splits: `--near-dup-cosine 0.98` (then filter
judgments/responses to kept `request_id`s).

### 7.8 Train

```bash
lrp train \
  --features "$LRP_DATA_DIR/features.parquet" \
  --judgments "$LRP_DATA_DIR/judgments.ndjson" \
  --responses "$LRP_DATA_DIR/responses.ndjson" \
  --embedding-backend onnxruntime \
  --embedding-model "$LRP_DATA_DIR/embed/model.onnx" \
  --tokenizer "$LRP_DATA_DIR/embed/tokenizer.json" \
  --device cuda:0 \
  --strict-device \
  --anchor-provider baseten \
  --anchor zai-org/GLM-5.2 \
  --ensemble-size 5 \
  --seed 42 \
  --out "$LRP_DATA_DIR/bundles"
```

Set `LRP_BUNDLE_DIR` to the printed `bundles/<version>` directory. Manifest
records embedding fingerprint, `train_device_class`, and
`intended_serve_device_class`.

For an Astra-anchored experiment, use `--anchor-provider openai --anchor gpt-6-astra`
and ensure that target appears in judgments/responses with enough train rows.

### 7.9 Eval + validate

```bash
# Start from services/learned-routing-policy/lrp.example.yaml -> private lrp.yaml
lrp eval \
  --bundle "$LRP_BUNDLE_DIR" \
  --features "$LRP_DATA_DIR/features.parquet" \
  --judgments "$LRP_DATA_DIR/judgments.ndjson" \
  --responses "$LRP_DATA_DIR/responses.ndjson" \
  --config "$LRP_DATA_DIR/lrp.yaml" \
  --out "$LRP_DATA_DIR/eval_report.json"

lrp validate --bundle "$LRP_BUNDLE_DIR"
# Optional:
# lrp sign-bundle --bundle "$LRP_BUNDLE_DIR" --key "$SIGNING_KEY" --key-id ops-1
# lrp verify-bundle --bundle "$LRP_BUNDLE_DIR" --trust "$TRUST_JSON" --require-signed
```

Read all six holdout baselines (always-cheap, anchor, LRP, oracle, …). Failed
gates stay failed. Synthetic demo success does not authorize promotion.

---

## 8. Deploy beside the router

### 8.1 LRP service config (`lrp.yaml`)

Start from `services/learned-routing-policy/lrp.example.yaml`:

```yaml
groups:
  your-coding-group:          # must match the router model group name callers use
    quality_floor: 0.80
    explore_rate: 0.0
    mode: enforce             # LRP recommendation mode; router owns shadow switch
deadline_ms: 500              # 1..4500; default 200
embedding:
  backend: onnxruntime
  model: /operator-owned/embed/model.onnx
  max_seq_len: 512
  batch_size: 1
compute:
  device: cuda:0
  strict_device: true
```

### 8.2 Start LRP

```bash
export LRP_POLICY_AUTH_HEADER='...'   # secret value for X-LRP-Auth

lrp serve \
  --bundle "$LRP_BUNDLE_DIR" \
  --config "$LRP_DATA_DIR/lrp.yaml" \
  --port 18093 \
  --admin-port 18094 \
  --deadline-ms 500
# Add --trust / --require-signed when using signed bundles
```

### 8.3 Wire the router model group

On the **matching** router group:

```yaml
strategy: external
include_request: true
external_policy:
  url: http://127.0.0.1:18093/route
  allow_hosts: [127.0.0.1]
  headers:
    X-LRP-Auth: ${LRP_POLICY_AUTH_HEADER}
  timeout_ms: 750          # > LRP deadline_ms; router max 5000
  mode: shadow             # start shadow; flip to enforce after gates
```

Rollout sequence:

1. `mode: shadow` — configured target serves; LRP recommendation recorded.
2. Inspect decision telemetry (`request_policy_executions`,
   `request_routing_decisions`, attempts, fallbacks).
3. `mode: enforce` only after workload + security acceptance.
4. Keep prior bundle for atomic rollback.

Optional admin explain (controlled envs):
`lrp serve --enable-admin` and authenticated `/explain` with the same normalized
policy payload (not a raw Chat body).

---

## 9. Results from this example run

### 9.1 Paid fanout economics (executed portfolio)

| Metric | Value |
| --- | --- |
| Estimate (printed first) | 7.105 USD |
| Abort threshold | 100 USD |
| Actual complete spend | **3.262 USD** |
| Response rows | 500 (100 × 5) |
| Status | ok 349 / upstream_error 151 (`http_429`) |

| Target | ok / 100 | Spend USD |
| --- | ---: | ---: |
| qwen/qwen3.5-9b | 75 | 0.149 |
| qwen/qwen3.8-27b | 73 | 0.283 |
| minimax/minimax-m3 | 67 | 0.241 |
| Fireworks kimi | 67 | 1.009 |
| Baseten GLM-5.2 | 67 | 1.579 |

Absolute gap on this sample (ok-row spend): GLM **1.579** − Qwen 3.5 **0.149** =
**1.430 USD**. That is room for a quality-aware mix — only if cheap targets clear
your floor. Report absolute USD and token categories; **no savings percentages**.

### 9.2 Sidecar throughput (synthetic_upstream sink)

| Conc | CPU C dec/s | GPU C dec/s | CPU sidecar p50 ms | GPU sidecar p50 ms |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 9.94 | 10.17 | 11.0 | 9.0 |
| 8 | 9.33 | 10.82 | 11.0 | 8.0 |
| 32 | 9.21 | 11.15 | 7.0 | 5.5 |
| 128 | 9.94 | 10.84 | 3.0 | 2.5 |

GPU train: ONNX int8 BGE, `cuda:0`, `strict_device=true`, ORT 1.22.0,
~4.4 s wall for this bundle size.

Machine-readable scalars (when you want them):  
`docs/evidence/learned-routing-policy/seed-example-corpus.json`,  
`docs/evidence/learned-routing-policy/routing-benchmark.json`.

---

## 10. Adapt checklist (your situation)

1. [ ] Define acceptance tests for **your** jobs (not only teacher-forced match).
2. [ ] Validate each candidate with direct + router smokes; record dialect/tools.
3. [ ] One router group per candidate; reusable calibration caller.
4. [ ] Private `LRP_DATA_DIR`; approve content transfer explicitly.
5. [ ] Dated prices in descriptors; print spend estimate; set dashboard caps.
6. [ ] Optional: add `gpt-6-astra` (or your frontier) as absolute-USD baseline;
      refresh OpenAI primary pricing the day you run.
7. [ ] Remote GPU featurize/train with `--device` + `--strict-device` as needed.
8. [ ] `lrp eval` six baselines; keep failed gates failed.
9. [ ] Deploy `lrp serve` + router `external_policy` in **shadow**, then enforce.
10. [ ] Tear down ephemeral GPUs; rotate `LRP_POLICY_AUTH_HEADER` and signing keys.

---

## 11. Limits (state these when you share)

- N=100 example budget ≠ production sufficiency.
- Teacher-forced continuation ≠ end-to-end agent success.
- `http_429` / `http_502` are failed rows, not free wins.
- GPT-6 Astra prices above are **illustrative** for adaptation; this run did not
  call Astra.
- Changing models, prices, or workload family requires fresh fanout and often a
  retrain.

---

## 12. Short share blurb (optional)

> Repeatable Metrum AI Router LRP example: OpenHands coding turns → multi-target
> fanout (Qwen / MiniMax / Kimi / GLM) → GPU train → shadow/enforce. Absolute
> spend ~$3.26 on 100×5; GLM vs cheap Qwen gap ~$1.43 on ok rows; enforce ~10
> decisions/s. Includes how to slot GPT-6 Astra ($10/$50 per 1M short-context)
> as a frontier baseline. Full CLI: `docs/share/lrp-coding-mix-example.md`.

© 2026 Metrum AI · Apache-2.0
