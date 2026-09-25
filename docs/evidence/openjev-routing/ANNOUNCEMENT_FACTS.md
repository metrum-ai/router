# OpenJev routing — raw facts for announcement distillation

All numbers below are from the live Shadeform run on 2026-09-25, not synthetic.
Benchmark commit pointer: `a9b4f12693fa039b00b0debd4e31d98e9179ced7`.

## Hardware / software (observed)

- Cloud/SKU: Shadeform `massedcompute` / `RTXPro6000`
- Note: Shadeform massedcompute RTXPro6000; NVIDIA RTX PRO 6000 Blackwell Server Edition 97887 MiB; driver 580.126.09; vLLM 0.29.0 fp8; CUDA_HOME=/usr/local/cuda-13.0
- OpenJev: `openjev/openjev` via vLLM serve name `qwen`, shim `POST /v1/systemone`
- Calibration env used: `READOUT_T=0.85`, `READOUT_NOUL_T=1.829074`, `READOUT_NOUL_BIAS=0`, `READOUT_TARGETED=1`, `READOUT_INSTR_STYLE=pyrepr`, `SHIM_STAGGER=1`
- Warmup OpenJev latency: **129 ms**

## Model ladder (live OpenAI key)

| fixture class | routed model | $/1M in | $/1M out |
|---|---|---:|---:|
| simple | gpt-5.6-luna | 0.20 | 1.20 |
| medium | gpt-5.6-sol | 4.00 | 20.00 |
| advanced | gpt-6-astra | 10.00 | 50.00 |

Key listed `gpt-6-astra` but not `gpt-6-luna` / `gpt-6-sol`; used GPT-5.6 Luna/Sol as cheap/mid.

## Aggregate (60 prompts × 2 repeats)

- Routing-label accuracy: **93.3%** (56/60)
- By fixture label: simple 95%, medium 85%, advanced 100%
- Tier mix: {'gpt-5.6-luna': 19, 'gpt-5.6-sol': 18, 'gpt-6-astra': 23}
- OpenJev latency ms: p50=193, mean=193, max=254
- Policy e2e ms: p50=194, mean=194, max=255
- Escalations: 4
- Repeat flips: 0 / 60 (shuffle_repeat=2)

## OpenAI cost/latency sample (15 chat.completions, max_completion_tokens=96)

- OK: 15/15, total sample cost USD **0.035115**
- `gpt-5.6-luna`: n=5, avg_latency_ms=1402, cost_usd=9.5e-05
- `gpt-5.6-sol`: n=5, avg_latency_ms=2387, cost_usd=0.00994
- `gpt-6-astra`: n=5, avg_latency_ms=5355, cost_usd=0.02508
- Projected 60-set cost from mean per-model sample cost: {'always_cheap': 0.00114, 'always_medium': 0.11928, 'always_advanced': 0.30096, 'openjev_routed_est': 0.151513}

### Per-call OpenAI sample rows

| id | label | model | latency_ms | in_tok | out_tok | cost_usd | finish |
|---|---|---|---:|---:|---:|---:|---|
| s01 | simple | gpt-5.6-luna | 1443 | 26 | 13 | 2.1e-05 | stop |
| s02 | simple | gpt-5.6-luna | 1617 | 25 | 12 | 1.9e-05 | stop |
| s03 | simple | gpt-5.6-luna | 1822 | 28 | 4 | 1e-05 | stop |
| s04 | simple | gpt-5.6-luna | 1127 | 20 | 12 | 1.8e-05 | stop |
| s05 | simple | gpt-5.6-luna | 1002 | 29 | 18 | 2.7e-05 | stop |
| m01 | medium | gpt-5.6-sol | 2350 | 21 | 96 | 0.002004 | length |
| m02 | medium | gpt-5.6-sol | 2175 | 28 | 96 | 0.002032 | length |
| m03 | medium | gpt-5.6-sol | 2406 | 21 | 91 | 0.001904 | stop |
| m04 | medium | gpt-5.6-sol | 2315 | 21 | 96 | 0.002004 | length |
| m05 | medium | gpt-5.6-sol | 2690 | 19 | 96 | 0.001996 | length |
| a01 | advanced | gpt-6-astra | 6818 | 24 | 96 | 0.00504 | length |
| a02 | advanced | gpt-6-astra | 4628 | 22 | 96 | 0.00502 | length |
| a03 | advanced | gpt-6-astra | 6491 | 19 | 96 | 0.00499 | length |
| a04 | advanced | gpt-6-astra | 3894 | 21 | 96 | 0.00501 | length |
| a05 | advanced | gpt-6-astra | 4946 | 22 | 96 | 0.00502 | length |

## All 60 routing decisions (first pass)

| id | fixture | openjev_choice | final_task | model | conf | escalated | highRisk | complexity | oj_ms | e2e_ms | correct |
|---|---|---|---|---|---|---|---|---:|---:|---:|---|
| a01 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.6856 | 195 | 196 | True |
| a02 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.7728 | 194 | 195 | True |
| a03 | advanced | advanced | advanced | `gpt-6-astra` | high | False | True | 3.8009 | 194 | 195 | True |
| a04 | advanced | advanced | advanced | `gpt-6-astra` | high | False | True | 3.312 | 193 | 194 | True |
| a05 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.4259 | 196 | 197 | True |
| a06 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.021 | 185 | 187 | True |
| a07 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.7752 | 192 | 193 | True |
| a08 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 2.6724 | 192 | 193 | True |
| a09 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.2191 | 191 | 192 | True |
| a10 | advanced | advanced | advanced | `gpt-6-astra` | high | False | True | 3.4727 | 192 | 193 | True |
| a11 | advanced | advanced | advanced | `gpt-6-astra` | high | False | True | 3.5926 | 192 | 193 | True |
| a12 | advanced | advanced | advanced | `gpt-6-astra` | mid | False | False | 2.8666 | 191 | 192 | True |
| a13 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.5619 | 193 | 194 | True |
| a14 | advanced | advanced | advanced | `gpt-6-astra` | mid | False | False | 2.6194 | 191 | 192 | True |
| a15 | advanced | advanced | advanced | `gpt-6-astra` | high | False | True | 2.9086 | 193 | 194 | True |
| a16 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.4401 | 192 | 193 | True |
| a17 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.6937 | 191 | 192 | True |
| a18 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 2.5479 | 189 | 190 | True |
| a19 | advanced | advanced | advanced | `gpt-6-astra` | high | False | True | 2.9319 | 194 | 195 | True |
| a20 | advanced | advanced | advanced | `gpt-6-astra` | high | False | False | 3.0671 | 192 | 193 | True |
| m01 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 1.7661 | 194 | 195 | True |
| m02 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 2.3367 | 198 | 199 | True |
| m03 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 1.9218 | 194 | 195 | True |
| m04 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 1.7259 | 194 | 195 | True |
| m05 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 1.9195 | 199 | 200 | True |
| m06 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 1.9143 | 195 | 196 | True |
| m07 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 1.5657 | 199 | 200 | True |
| m08 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 2.6318 | 194 | 196 | True |
| m09 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 2.1499 | 189 | 190 | True |
| m10 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 2.1731 | 191 | 192 | True |
| m11 | medium | medium | medium | `gpt-5.6-sol` | mid | False | False | 2.0571 | 195 | 196 | True |
| m12 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 1.818 | 194 | 195 | True |
| m13 | medium | medium | advanced | `gpt-6-astra` | low | True | True | 2.6448 | 194 | 195 | False |
| m14 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 2.1691 | 186 | 187 | True |
| m15 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 1.7747 | 187 | 188 | True |
| m16 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 2.1888 | 194 | 195 | True |
| m17 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 2.2108 | 191 | 192 | True |
| m18 | medium | medium | medium | `gpt-5.6-sol` | high | False | False | 2.4398 | 195 | 196 | True |
| m19 | medium | medium | advanced | `gpt-6-astra` | high | True | True | 1.482 | 196 | 197 | False |
| m20 | medium | medium | advanced | `gpt-6-astra` | low | True | False | 2.8033 | 188 | 190 | False |
| s01 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.6854 | 186 | 188 | True |
| s02 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 1.1968 | 194 | 195 | True |
| s03 | simple | simple | medium | `gpt-5.6-sol` | high | True | True | 0.8724 | 254 | 255 | False |
| s04 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.9604 | 191 | 193 | True |
| s05 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.8351 | 192 | 193 | True |
| s06 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 1.0022 | 193 | 194 | True |
| s07 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.4338 | 192 | 193 | True |
| s08 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.817 | 192 | 193 | True |
| s09 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.873 | 190 | 191 | True |
| s10 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.8987 | 190 | 191 | True |
| s11 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.9099 | 195 | 196 | True |
| s12 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.7724 | 190 | 191 | True |
| s13 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.9534 | 191 | 192 | True |
| s14 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.6972 | 191 | 192 | True |
| s15 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.6924 | 190 | 191 | True |
| s16 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 1.1264 | 193 | 195 | True |
| s17 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.6187 | 201 | 202 | True |
| s18 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 1.3977 | 196 | 197 | True |
| s19 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.8249 | 193 | 194 | True |
| s20 | simple | simple | simple | `gpt-5.6-luna` | high | False | False | 0.9304 | 202 | 204 | True |

## Prompt → decision detail (verbatim prompts)

### a01 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Prove the multi-constraint trade-offs between consistency and latency for a global ledger design.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.6856
- latency: openjev=195 ms, policy_e2e=196 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=194

### a02 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Research ambiguous failure modes when combining exactly-once semantics with multi-region failover.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.7728
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=195

### a03 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Architect a formal verification plan for a consensus protocol under Byzantine faults.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=True, complexity=3.8009
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=192

### a04 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: High-stakes legal analysis: compare liability exposure across three data-processing clauses.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=True, complexity=3.312
- latency: openjev=193 ms, policy_e2e=194 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=191

### a05 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Design a theorem-level argument for why this cache invalidation scheme can lose writes.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.4259
- latency: openjev=196 ms, policy_e2e=197 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=189

### a06 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Evaluate research trade-offs for training a routing policy with delayed outcome labels.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.021
- latency: openjev=185 ms, policy_e2e=187 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=186

### a07 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Multi-constraint system design: privacy, cost, and p99 latency under bursty agent traffic.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.7752
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=192

### a08 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Ambiguous research question: when does tool-calling outweigh longer context for accuracy?
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.6724
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=191

### a09 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Prove whether this scheduling policy is starvation-free under adversarial arrivals.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.2191
- latency: openjev=191 ms, policy_e2e=192 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=193

### a10 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: High-stakes architecture review: blast radius of a shared secrets store across tenants.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=True, complexity=3.4727
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=192

### a11 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Formal verification sketch for a payment state machine with compensating transactions.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=True, complexity=3.5926
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=191

### a12 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Research design: measure whether decision models beat LLM classifiers for routing cost.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=mid, escalated=False, highRisk=False, complexity=2.8666
- latency: openjev=191 ms, policy_e2e=192 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=191

### a13 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Multi-constraint optimization: allocate GPU capacity across training and serving under SLA risk.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.5619
- latency: openjev=193 ms, policy_e2e=194 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=191

### a14 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Argue the trade-offs of shadow vs enforce modes for an external routing policy rollout.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=mid, escalated=False, highRisk=False, complexity=2.6194
- latency: openjev=191 ms, policy_e2e=192 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=194

### a15 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: High-stakes incident analysis: reconstruct a cascading outage from partial telemetry.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=True, complexity=2.9086
- latency: openjev=193 ms, policy_e2e=194 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=191

### a16 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Architect a research evaluation that separates routing quality from upstream model quality.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.4401
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=192

### a17 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Prove limits of calibrated probabilities when option order is adversarially shuffled.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.6937
- latency: openjev=191 ms, policy_e2e=192 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=193

### a18 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Ambiguous design: should long documents be chunked before a decision model or after?
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.5479
- latency: openjev=189 ms, policy_e2e=190 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=190

### a19 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Multi-constraint legal and safety review for releasing an open decision model weights demo.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=True, complexity=2.9319
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=191

### a20 [OK] fixture=`advanced` → `gpt-6-astra`

- Prompt: Research-grade comparison plan for always-flagship vs task-routed OpenAI spend.
- OpenJev choice: `advanced` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=3.0671
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=192

### m01 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Implement a Python function that merges two sorted lists and include a unit test.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.7661
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=187

### m02 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Write a SQL query to find users who purchased in the last 30 days but not the prior 30.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.3367
- latency: openjev=198 ms, policy_e2e=199 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=198

### m03 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Refactor this pseudocode into a clear FastAPI endpoint with input validation.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.9218
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=195

### m04 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Debug why this binary search fails on an empty array and propose a fix.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.7259
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=197

### m05 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Analyze the time complexity of this nested loop and suggest one optimization.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.9195
- latency: openjev=199 ms, policy_e2e=200 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=197

### m06 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Implement a rate limiter in Python using a token bucket; keep it under 40 lines.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.9143
- latency: openjev=195 ms, policy_e2e=196 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=197

### m07 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Write code to parse an OpenAPI path parameter and reject empty values.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.5657
- latency: openjev=199 ms, policy_e2e=200 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=197

### m08 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Design a bounded coding task solution: CSV to JSON converter with schema checks.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.6318
- latency: openjev=194 ms, policy_e2e=196 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=195

### m09 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Unit test a retry helper that backs off exponentially up to three attempts.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.1499
- latency: openjev=189 ms, policy_e2e=190 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=195

### m10 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Implement pagination helpers for an HTTP API client with next-page tokens.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.1731
- latency: openjev=191 ms, policy_e2e=192 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=194

### m11 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Analyze this log sample and identify the most likely root cause of timeouts.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=mid, escalated=False, highRisk=False, complexity=2.0571
- latency: openjev=195 ms, policy_e2e=196 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=195

### m12 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Write a Python script to benchmark JSON encode/decode for 10k records.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.818
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=194

### m13 [MISS] fixture=`medium` → `gpt-6-astra`

- Prompt: Implement idempotency-key handling for a create-order API.
- OpenJev choice: `medium` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=low, escalated=True, highRisk=True, complexity=2.6448
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=193

### m14 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Refactor duplicated validation logic into a shared function with tests.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.1691
- latency: openjev=186 ms, policy_e2e=187 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=188

### m15 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Write SQL to detect duplicate invoices by customer_id and invoice_date.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.7747
- latency: openjev=187 ms, policy_e2e=188 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=195

### m16 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Implement a small CLI that diffs two directories and prints changed file paths.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.1888
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=167

### m17 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Debug an off-by-one error in a sliding window maximum algorithm.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.2108
- latency: openjev=191 ms, policy_e2e=192 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=194

### m18 [OK] fixture=`medium` → `gpt-5.6-sol`

- Prompt: Analyze API latency percentiles and recommend one caching change.
- OpenJev choice: `medium` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=2.4398
- latency: openjev=195 ms, policy_e2e=196 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=196

### m19 [MISS] fixture=`medium` → `gpt-6-astra`

- Prompt: Implement JWT expiry checking without verifying signatures in this sandbox.
- OpenJev choice: `medium` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=high, escalated=True, highRisk=True, complexity=1.482
- latency: openjev=196 ms, policy_e2e=197 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=195

### m20 [MISS] fixture=`medium` → `gpt-6-astra`

- Prompt: Write code for a worker that claims jobs from a queue with lease renewal.
- OpenJev choice: `medium` → policy final task: `advanced` (classLabel `openjev:advanced`)
- confidenceBand=low, escalated=True, highRisk=False, complexity=2.8033
- latency: openjev=188 ms, policy_e2e=190 ms; repeat_targetIndex=2 flip=False repeat_oj_ms=195

### s01 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Summarize this product update in one sentence: we shipped dark mode and fixed login timeouts.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.6854
- latency: openjev=186 ms, policy_e2e=188 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=193

### s02 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Extract the email addresses from: Alice <a@ex.com>, Bob bob@ex.org.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.1968
- latency: openjev=194 ms, policy_e2e=195 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=286

### s03 [MISS] fixture=`simple` → `gpt-5.6-sol`

- Prompt: Classify this ticket as billing, shipping, or technical: I was charged twice for order 4412.
- OpenJev choice: `simple` → policy final task: `medium` (classLabel `openjev:medium`)
- confidenceBand=high, escalated=True, highRisk=True, complexity=0.8724
- latency: openjev=254 ms, policy_e2e=255 ms; repeat_targetIndex=1 flip=False repeat_oj_ms=197

### s04 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Rewrite this bluntly but politely: Your docs are confusing and incomplete.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.9604
- latency: openjev=191 ms, policy_e2e=193 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=186

### s05 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: TL;DR these bullets into a short summary: we met quota; churn fell 2%; NPS rose.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.8351
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=171

### s06 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Translate to Spanish, keep it one sentence: The meeting was postponed until Friday.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.0022
- latency: openjev=193 ms, policy_e2e=194 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=197

### s07 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Classify sentiment as positive, neutral, or negative: Thanks, that fixed it!
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.4338
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=192

### s08 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Summarize the following changelog entry for customers: fixed race in cache invalidation.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.817
- latency: openjev=192 ms, policy_e2e=193 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=192

### s09 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Extract JSON fields name and amount from: name=Ada amount=42 currency=USD.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.873
- latency: openjev=190 ms, policy_e2e=191 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=190

### s10 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Rewrite this subject line to be shorter: Important update regarding your upcoming invoice.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.8987
- latency: openjev=190 ms, policy_e2e=191 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=194

### s11 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Classify language: Bonjour, je voudrais un remboursement.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.9099
- latency: openjev=195 ms, policy_e2e=196 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=194

### s12 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: One-sentence summary of: rain delayed the outdoor launch event by two hours.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.7724
- latency: openjev=190 ms, policy_e2e=191 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=189

### s13 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Extract action items as bullets: Call Sam, file the expense, and book travel.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.9534
- latency: openjev=191 ms, policy_e2e=192 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=192

### s14 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Classify priority low/medium/high: password reset email is delayed by 10 minutes.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.6972
- latency: openjev=191 ms, policy_e2e=192 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=189

### s15 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Summarize for an executive: weekly active users grew 4% week over week.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.6924
- latency: openjev=190 ms, policy_e2e=191 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=192

### s16 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Rewrite in plain English: We will remediate the observability gap in Q3.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.1264
- latency: openjev=193 ms, policy_e2e=195 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=192

### s17 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Extract dates: kickoff on 2026-09-01 and GA on 2026-10-15.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.6187
- latency: openjev=201 ms, policy_e2e=202 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=200

### s18 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Classify intent: Where is my package tracking number for order 88?
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=1.3977
- latency: openjev=196 ms, policy_e2e=197 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=195

### s19 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Bullet-summarize: hiring two engineers; freezing marketing spend; expanding support hours.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.8249
- latency: openjev=193 ms, policy_e2e=194 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=192

### s20 [OK] fixture=`simple` → `gpt-5.6-luna`

- Prompt: Summarize this error for a non-engineer: NullPointerException in CheckoutService.
- OpenJev choice: `simple` → policy final task: `simple` (classLabel `openjev:simple`)
- confidenceBand=high, escalated=False, highRisk=False, complexity=0.9304
- latency: openjev=202 ms, policy_e2e=204 ms; repeat_targetIndex=0 flip=False repeat_oj_ms=194

## Full System One traces (4 filmed examples)

### Trace `s01` — OpenJev wall time 177 ms

- state: Summarize this product update in one sentence: we shipped dark mode and fixed login timeouts.
- task.choice=simple confidence=0.993 probabilities={'simple': 0.9953, 'medium': 0.0043, 'advanced': 0.0004}
- complexity.score=0.6159 confidence=0.6709 probabilities={'0': 0.3895, '1': 0.6055, '2': 0.0047, '3': 0.0001, '4': 0.0001}
- high_risk.noul=0.0071
- usage={'input_tokens': 330, 'output_tokens': 0}

```json
{
  "request": {
    "model": "openjev",
    "state": "Summarize this product update in one sentence: we shipped dark mode and fixed login timeouts.",
    "questions": {
      "task": {
        "type": "choice",
        "instructions": "Classify the user request into exactly one task class for model routing. simple = short summarization, extraction, classification, rewrite, or other high-volume low-stakes text work. medium = bounded coding, analysis, multi-step but well-scoped work. advanced = hard reasoning, ambiguous research, multi-constraint design, or high-stakes judgment.",
        "criteria": {
          "simple": null,
          "medium": null,
          "advanced": null
        }
      },
      "complexity": {
        "type": "score",
        "instructions": "How complex is this request?",
        "criteria": [
          "trivial",
          "light",
          "moderate",
          "hard",
          "expert"
        ]
      },
      "high_risk": {
        "type": "noul",
        "instructions": "Is this high-risk if answered poorly (safety, legal, irreversible actions, or material money loss)?"
      }
    }
  },
  "response": {
    "id": "shim-1790362242765",
    "model": "openjev T=0.85 noul=1.829074,0.0 flags={\"perms\":1,\"stagger\":true,\"loop_break\":false,\"compact\":false,\"compact_cap\":0,\"layout\":\"\",\"pad\":0,\"targeted\":true,\"instr_style\":\"pyrepr\"} shim=shim.py@81a22f1b1b89",
    "answers": {
      "task": {
        "type": "choice",
        "choice": "simple",
        "probabilities": {
          "simple": 0.9953,
          "medium": 0.0043,
          "advanced": 0.0004
        },
        "confidence": 0.993
      },
      "complexity": {
        "type": "score",
        "score": 0.6159,
        "legend": {
          "0": "trivial",
          "1": "light",
          "2": "moderate",
          "3": "hard",
          "4": "expert"
        },
        "probabilities": {
          "0": 0.3895,
          "1": 0.6055,
          "2": 0.0047,
          "3": 0.0001,
          "4": 0.0001
        },
        "confidence": 0.6709
      },
      "high_risk": {
        "type": "noul",
        "noul": 0.0071
      }
    },
    "usage": {
      "input_tokens": 330,
      "output_tokens": 0
    }
  }
}
```

### Trace `m01` — OpenJev wall time 191 ms

- state: Implement a Python function that merges two sorted lists and include a unit test.
- task.choice=medium confidence=0.9984 probabilities={'simple': 0.0005, 'medium': 0.9989, 'advanced': 0.0006}
- complexity.score=1.7387 confidence=0.7723 probabilities={'0': 0.0032, '1': 0.2609, '2': 0.7305, '3': 0.0049, '4': 0.0005}
- high_risk.noul=0.0065
- usage={'input_tokens': 318, 'output_tokens': 0}

```json
{
  "request": {
    "model": "openjev",
    "state": "Implement a Python function that merges two sorted lists and include a unit test.",
    "questions": {
      "task": {
        "type": "choice",
        "instructions": "Classify the user request into exactly one task class for model routing. simple = short summarization, extraction, classification, rewrite, or other high-volume low-stakes text work. medium = bounded coding, analysis, multi-step but well-scoped work. advanced = hard reasoning, ambiguous research, multi-constraint design, or high-stakes judgment.",
        "criteria": {
          "simple": null,
          "medium": null,
          "advanced": null
        }
      },
      "complexity": {
        "type": "score",
        "instructions": "How complex is this request?",
        "criteria": [
          "trivial",
          "light",
          "moderate",
          "hard",
          "expert"
        ]
      },
      "high_risk": {
        "type": "noul",
        "instructions": "Is this high-risk if answered poorly (safety, legal, irreversible actions, or material money loss)?"
      }
    }
  },
  "response": {
    "id": "shim-1790362242956",
    "model": "openjev T=0.85 noul=1.829074,0.0 flags={\"perms\":1,\"stagger\":true,\"loop_break\":false,\"compact\":false,\"compact_cap\":0,\"layout\":\"\",\"pad\":0,\"targeted\":true,\"instr_style\":\"pyrepr\"} shim=shim.py@81a22f1b1b89",
    "answers": {
      "task": {
        "type": "choice",
        "choice": "medium",
        "probabilities": {
          "simple": 0.0005,
          "medium": 0.9989,
          "advanced": 0.0006
        },
        "confidence": 0.9984
      },
      "complexity": {
        "type": "score",
        "score": 1.7387,
        "legend": {
          "0": "trivial",
          "1": "light",
          "2": "moderate",
          "3": "hard",
          "4": "expert"
        },
        "probabilities": {
          "0": 0.0032,
          "1": 0.2609,
          "2": 0.7305,
          "3": 0.0049,
          "4": 0.0005
        },
        "confidence": 0.7723
      },
      "high_risk": {
        "type": "noul",
        "noul": 0.0065
      }
    },
    "usage": {
      "input_tokens": 318,
      "output_tokens": 0
    }
  }
}
```

### Trace `a01` — OpenJev wall time 188 ms

- state: Prove the multi-constraint trade-offs between consistency and latency for a global ledger design.
- task.choice=advanced confidence=0.994 probabilities={'simple': 0.0012, 'medium': 0.0028, 'advanced': 0.996}
- complexity.score=3.6135 confidence=0.6779 probabilities={'0': 0.0119, '1': 0.0057, '2': 0.02, '3': 0.2817, '4': 0.6807}
- high_risk.noul=0.0163
- usage={'input_tokens': 327, 'output_tokens': 0}

```json
{
  "request": {
    "model": "openjev",
    "state": "Prove the multi-constraint trade-offs between consistency and latency for a global ledger design.",
    "questions": {
      "task": {
        "type": "choice",
        "instructions": "Classify the user request into exactly one task class for model routing. simple = short summarization, extraction, classification, rewrite, or other high-volume low-stakes text work. medium = bounded coding, analysis, multi-step but well-scoped work. advanced = hard reasoning, ambiguous research, multi-constraint design, or high-stakes judgment.",
        "criteria": {
          "simple": null,
          "medium": null,
          "advanced": null
        }
      },
      "complexity": {
        "type": "score",
        "instructions": "How complex is this request?",
        "criteria": [
          "trivial",
          "light",
          "moderate",
          "hard",
          "expert"
        ]
      },
      "high_risk": {
        "type": "noul",
        "instructions": "Is this high-risk if answered poorly (safety, legal, irreversible actions, or material money loss)?"
      }
    }
  },
  "response": {
    "id": "shim-1790362243145",
    "model": "openjev T=0.85 noul=1.829074,0.0 flags={\"perms\":1,\"stagger\":true,\"loop_break\":false,\"compact\":false,\"compact_cap\":0,\"layout\":\"\",\"pad\":0,\"targeted\":true,\"instr_style\":\"pyrepr\"} shim=shim.py@81a22f1b1b89",
    "answers": {
      "task": {
        "type": "choice",
        "choice": "advanced",
        "probabilities": {
          "simple": 0.0012,
          "medium": 0.0028,
          "advanced": 0.996
        },
        "confidence": 0.994
      },
      "complexity": {
        "type": "score",
        "score": 3.6135,
        "legend": {
          "0": "trivial",
          "1": "light",
          "2": "moderate",
          "3": "hard",
          "4": "expert"
        },
        "probabilities": {
          "0": 0.0119,
          "1": 0.0057,
          "2": 0.02,
          "3": 0.2817,
          "4": 0.6807
        },
        "confidence": 0.6779
      },
      "high_risk": {
        "type": "noul",
        "noul": 0.0163
      }
    },
    "usage": {
      "input_tokens": 327,
      "output_tokens": 0
    }
  }
}
```

### Trace `s03` — OpenJev wall time 188 ms

- state: Classify this ticket as billing, shipping, or technical: I was charged twice for order 4412.
- task.choice=simple confidence=0.997 probabilities={'simple': 0.998, 'medium': 0.0015, 'advanced': 0.0004}
- complexity.score=1.0115 confidence=0.9005 probabilities={'0': 0.054, '1': 0.8821, '2': 0.0625, '3': 0.0013, '4': 0.0001}
- high_risk.noul=0.9717
- usage={'input_tokens': 345, 'output_tokens': 0}

```json
{
  "request": {
    "model": "openjev",
    "state": "Classify this ticket as billing, shipping, or technical: I was charged twice for order 4412.",
    "questions": {
      "task": {
        "type": "choice",
        "instructions": "Classify the user request into exactly one task class for model routing. simple = short summarization, extraction, classification, rewrite, or other high-volume low-stakes text work. medium = bounded coding, analysis, multi-step but well-scoped work. advanced = hard reasoning, ambiguous research, multi-constraint design, or high-stakes judgment.",
        "criteria": {
          "simple": null,
          "medium": null,
          "advanced": null
        }
      },
      "complexity": {
        "type": "score",
        "instructions": "How complex is this request?",
        "criteria": [
          "trivial",
          "light",
          "moderate",
          "hard",
          "expert"
        ]
      },
      "high_risk": {
        "type": "noul",
        "instructions": "Is this high-risk if answered poorly (safety, legal, irreversible actions, or material money loss)?"
      }
    }
  },
  "response": {
    "id": "shim-1790362243333",
    "model": "openjev T=0.85 noul=1.829074,0.0 flags={\"perms\":1,\"stagger\":true,\"loop_break\":false,\"compact\":false,\"compact_cap\":0,\"layout\":\"\",\"pad\":0,\"targeted\":true,\"instr_style\":\"pyrepr\"} shim=shim.py@81a22f1b1b89",
    "answers": {
      "task": {
        "type": "choice",
        "choice": "simple",
        "probabilities": {
          "simple": 0.998,
          "medium": 0.0015,
          "advanced": 0.0004
        },
        "confidence": 0.997
      },
      "complexity": {
        "type": "score",
        "score": 1.0115,
        "legend": {
          "0": "trivial",
          "1": "light",
          "2": "moderate",
          "3": "hard",
          "4": "expert"
        },
        "probabilities": {
          "0": 0.054,
          "1": 0.8821,
          "2": 0.0625,
          "3": 0.0013,
          "4": 0.0001
        },
        "confidence": 0.9005
      },
      "high_risk": {
        "type": "noul",
        "noul": 0.9717
      }
    },
    "usage": {
      "input_tokens": 345,
      "output_tokens": 0
    }
  }
}
```

## The 4 misses (fixture label vs routed)

- **m13**: fixture `medium` but routed `advanced` → `gpt-6-astra` because escalated=True highRisk=True confidenceBand=low (OpenJev first choice `medium`). Prompt: Implement idempotency-key handling for a create-order API.
- **m19**: fixture `medium` but routed `advanced` → `gpt-6-astra` because escalated=True highRisk=True confidenceBand=high (OpenJev first choice `medium`). Prompt: Implement JWT expiry checking without verifying signatures in this sandbox.
- **m20**: fixture `medium` but routed `advanced` → `gpt-6-astra` because escalated=True highRisk=False confidenceBand=low (OpenJev first choice `medium`). Prompt: Write code for a worker that claims jobs from a queue with lease renewal.
- **s03**: fixture `simple` but routed `medium` → `gpt-5.6-sol` because escalated=True highRisk=True confidenceBand=high (OpenJev first choice `simple`). Prompt: Classify this ticket as billing, shipping, or technical: I was charged twice for order 4412.

## Distill tips

- Lead with one simple / one medium / one advanced verbatim prompt + model + ms + confidence.
- Mention s03 as the honest miss: billing charge → OpenJev said simple but noul high → policy escalated to sol.
- Cost line: always-astra projected $0.30096 vs routed $0.151513 on mean-sample projection.
- License: CC BY-NC 4.0 weights; OpenJev ≠ hosted Jev.
