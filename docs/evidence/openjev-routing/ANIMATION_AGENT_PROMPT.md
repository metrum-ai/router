# Agent prompt: animate OpenJev routing decision (observed reality only)

Use this prompt in a **separate agent / chat**. Do **not** invent timings, probabilities, model IDs, or UI states. Everything below was measured on 2026-09-25 against a live OpenJev System One shim on Shadeform.

## Goal

Build a short **animated** visual that shows one request flowing through Metrum AI Router’s external OpenJev policy and landing on one of three OpenAI models. Prefer a Canvas / web animation / motion graphic suitable for a social clip (~15–30s).

## Hard constraints

- Use **only** the measured numbers and JSON shapes in this prompt (and linked evidence files).
- Do **not** claim 100% accuracy; live label accuracy was **93.3%** (56/60).
- Disclose: OpenJev weights are **CC BY-NC 4.0**; OpenJev ≠ TypeSafe hosted Jev.
- Do **not** show API keys, SSH IPs, or raw private prompts beyond the fixture lines below.
- Prefer one composition, not a dashboard. Brand/product: **Metrum AI Router** + **OpenJev**.

## Measured system (reality)

Hardware / serve path:

- Shadeform `massedcompute` SKU `RTXPro6000`
- GPU: NVIDIA RTX PRO 6000 Blackwell Server Edition, **97887 MiB**, driver **580.126.09**
- Serve: `vllm==0.29.0` with `--quantization fp8`, localhost `:8000`, served name `qwen`
- Decision API: OpenJev `helper/shim.py` on localhost `:3000` (SSH tunnel to laptop)
- Required host fix observed: install `cuda-nvcc-13-0`, set `CUDA_HOME=/usr/local/cuda-13.0`, and `VLLM_USE_FLASHINFER_SAMPLER=0` (first boot failed with missing `nvcc`)
- Shim must be started with **`python3`** (bare `python` missing on image)

Model ladder actually used (live OpenAI key; `gpt-6-luna`/`gpt-6-sol` not listed):

| task class | target model | published $/1M in/out |
|---|---|---|
| simple | `gpt-5.6-luna` | 0.20 / 1.20 |
| medium | `gpt-5.6-sol` | 4.00 / 20.00 |
| advanced | `gpt-6-astra` | 10.00 / 50.00 |

Live suite results (`docs/evidence/openjev-routing/benchmark.live.json`):

- **60** labelled prompts, **93.3%** routing-label accuracy
- by label: simple **95%**, medium **85%**, advanced **100%**
- OpenJev latency p50 **193 ms** (mean 193, max 254)
- policy e2e p50 **194 ms**
- tier mix: luna **19**, sol **18**, astra **23**
- repeat stability: **0 flips** across 60 cases × 2 repeats
- escalations: **4** (mostly high-risk / low-confidence bumps)
- OpenAI cost sample 15 calls: **$0.035115** total; projected always-astra ≈ **$0.301** vs routed est ≈ **$0.152** on the 60-set mean-cost projection

## Exact decision flow to animate

1. **User request** arrives at router model group `openjev-routing-demo` (`strategy: external`, `include_request: true`).
2. Router POSTs eligible targets + trusted request text to policy `POST /route`.
3. Policy builds OpenJev `state` from request text and POSTs `POST /v1/systemone` with three questions in **one** request:
   - `task` choice: `simple` | `medium` | `advanced`
   - `complexity` score: trivial→expert (legend 0..4)
   - `high_risk` noul: calibrated P(yes) in field `noul`
4. OpenJev returns typed answers (no free-form generation; `usage.output_tokens: 0`).
5. Policy maps task→tier target by **tier/model id**, not array index; escalates **one tier** if `confidence < 0.45` or `noul >= 0.5`.
6. Policy returns `{targetIndex, fallbackIndexes, classLabel, metadata}` to router.
7. Router calls the selected OpenAI model (visual can stop at “selected model” or show a brief upstream latency chip from samples: luna ~1.4s, sol ~2.4s, astra ~5.4s avg on 96-token completions).

## Observed response shape (real)

```json
{
  "id": "shim-...",
  "model": "openjev T=0.85 noul=...",
  "answers": {
    "task": {
      "type": "choice",
      "choice": "simple",
      "probabilities": {"simple": 0.9715, "medium": 0.0183, "advanced": 0.0102},
      "confidence": 0.9572
    },
    "complexity": {
      "type": "score",
      "score": 0.687,
      "legend": {"0": "trivial", "1": "light", "2": "moderate", "3": "hard", "4": "expert"},
      "probabilities": {"0": 0.4268, "1": 0.4944, "2": 0.0545, "3": 0.0135, "4": 0.0108},
      "confidence": 0.5495
    },
    "high_risk": {"type": "noul", "noul": 0.0065}
  },
  "usage": {"input_tokens": 235, "output_tokens": 0}
}
```

Policy response shape (real):

```json
{
  "targetIndex": 0,
  "fallbackIndexes": [1, 2],
  "classLabel": "openjev:simple",
  "metadata": {
    "task": "simple",
    "requestedTask": "simple",
    "confidenceBand": "high",
    "escalated": false,
    "highRisk": false,
    "complexity": 0.6568,
    "openjevLatencyMs": 186
  }
}
```

## Three filmed examples (use these prompts verbatim)

### A — simple → Luna (~177–186 ms OpenJev)

Prompt: `Summarize this product update in one sentence: we shipped dark mode and fixed login timeouts.`

Observed: `choice=simple`, conf≈**0.993**, probs≈(0.997 / 0.002 / 0.000), score≈0.66, noul≈**0.007**, route → `gpt-5.6-luna`.

### B — medium → Sol (~191 ms)

Prompt: `Implement a Python function that merges two sorted lists and include a unit test.`

Observed: `choice=medium`, conf≈**0.998**, score≈1.74, noul≈**0.007**, route → `gpt-5.6-sol`.

### C — advanced → Astra (~188 ms)

Prompt: `Prove the multi-constraint trade-offs between consistency and latency for a global ledger design.`

Observed: `choice=advanced`, conf≈**0.994**, score≈3.61, noul≈**0.016**, route → `gpt-6-astra`.

### Optional fourth beat — escalation (observed mismatch)

Prompt: `Classify this ticket as billing, shipping, or technical: I was charged twice for order 4412.`

Observed OpenJev: often `choice=simple` with **noul≈0.97** (billing dispute read as high-risk). Policy escalates simple→**medium** → `gpt-5.6-sol`. This is one of the 4/60 “wrong vs fixture label” cases and is good for showing honesty.

## Suggested animation storyboard

1. (0–3s) Brand: Metrum AI Router. Subtitle: “OpenJev decides which GPT answers.”
2. (3–8s) Prompt A types in; packet flies to OpenJev node; probability bars fill to measured probs; checkmark simple; chip `193 ms`.
3. (8–12s) Arrow to `gpt-5.6-luna` with price chip `$0.20 / $1.20`.
4. (12–20s) Fast cuts of B→sol and C→astra with their measured confidences.
5. (20–26s) End card: **93.3%** label accuracy · **0** repeat flips · p50 **193 ms** · hardware Blackwell RTX PRO 6000 · CC BY-NC note · repo evidence path.

Motion: 2–3 intentional motions (packet travel, probability bar fill, target highlight). No emoji. No purple-glow AI cliché if avoidable.

## Source files in this repo

- Evidence: `docs/evidence/openjev-routing/benchmark.live.json`, `README.live.md`
- Policy: `examples/external-routing-policy/openjev_policy.py`
- Runbook: `docs/OPENJEV_ROUTING_DEMO.md`
- Local traces (gitignored): `docs/evidence/openjev-routing/observed-traces.local.json`

## Acceptance for the animation agent

- Every number on screen appears in `benchmark.live.json` or the filmed examples above.
- Shows task-class → priced target mapping, not “OpenJev picks gpt-6 by name.”
- Includes one escalation/high-risk beat or an explicit accuracy caveat.
- Ships with a one-paragraph caption ready for social, including license + “measured, not imagined.”
