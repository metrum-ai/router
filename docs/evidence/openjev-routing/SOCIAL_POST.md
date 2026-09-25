# LinkedIn draft (OpenJev external routing) — same register as last week’s LRP post

Paste below. Facts from the 2026-09-25 live Shadeform run. Receipts: `ANNOUNCEMENT_FACTS.md`, `routing-decisions.live.json`, `benchmark.live.json`.

---

Metrum AI Router’s routing layer is pluggable. Last week we showed LRP, a learned policy you train on your own traffic. This week is a different plugin: an external policy that asks OpenJev, an open-weights decision model, which *task class* a request is, then maps that class onto priced OpenAI targets. One full live run, written so you can reproduce it.

OpenJev does not write the answer. It returns typed decisions: a choice, a score, a yes/no probability. No free-form text to parse. The router still owns eligibility, the upstream call, usage, latency, and fallback. The policy cannot invent targets outside the group.

We served `openjev/openjev` ourselves on Shadeform `RTXPro6000` (NVIDIA RTX PRO 6000 Blackwell Server Edition, 97887 MiB, driver 580.126.09) with vLLM 0.29.0 FP8 and the upstream System One shim on localhost, reached over an SSH tunnel. Calibration flags matched the model card (`READOUT_T=0.85`, targeted readout on). Warmup decision: 129 ms.

The OpenAI key listed `gpt-6-astra` but not `gpt-6-luna` / `gpt-6-sol`, so the live ladder was:

- simple → `gpt-5.6-luna` at $0.20 / $1.20 per 1M in/out
- medium → `gpt-5.6-sol` at $4 / $20
- advanced → `gpt-6-astra` at $10 / $50

Sixty labelled prompts, 20 per class, each routed twice. Label accuracy 56/60 (93.3%): simple 95%, medium 85%, advanced 100%. Tier mix: luna 19, sol 18, astra 23. OpenJev latency p50 193 ms (max 254). Policy e2e p50 194 ms. Repeat flips: 0. Four escalations.

Concrete decisions, verbatim prompts:

“Summarize this product update in one sentence: we shipped dark mode and fixed login timeouts.” OpenJev choice `simple`, probabilities 0.9953 / 0.0043 / 0.0004, confidence 0.993, noul 0.007, complexity 0.62 → `gpt-5.6-luna` in 186 ms.

“Implement a Python function that merges two sorted lists and include a unit test.” Choice `medium`, 0.0005 / 0.9989 / 0.0006, confidence 0.998 → `gpt-5.6-sol` in 194 ms.

“Prove the multi-constraint trade-offs between consistency and latency for a global ledger design.” Choice `advanced`, 0.0012 / 0.0028 / 0.996, confidence 0.994 → `gpt-6-astra` in 195 ms.

We also show a miss on purpose. “Classify this ticket as billing, shipping, or technical: I was charged twice for order 4412.” OpenJev still chose `simple` (0.998 / 0.0015 / 0.0004) but noul was 0.972. The policy escalates one tier on high risk → `gpt-5.6-sol`. That is 1 of 4 fixture mismatches; the other three were medium prompts escalated to astra (idempotency-key handling, JWT expiry without signature verify, queue worker with lease renewal).

A separate OpenAI sample of 15 completions (5 per tier, max_completion_tokens 96) cost $0.035115. Mean per-model cost projected across the 60-prompt set: always-luna $0.00114, always-sol $0.11928, always-astra $0.30096, OpenJev-routed estimate $0.151513. We report absolute dollars, not savings percentages. That projection is not end-to-end agent quality.

Deployment is the existing external-policy contract: `strategy: external`, `include_request: true` only on a trusted policy host, start in `shadow`, promote to `enforce` after you accept the recommendations. Example config and Shadeform runbook are in the repo. OpenJev weights are CC BY-NC 4.0; this was a research/demo run, not a commercial license. OpenJev is independent of TypeSafe’s hosted Jev.

Limits: N=60 is a labelled fixture, not production traffic. Accuracy is task-class match to our labels, not graded answer quality. Rate limits and missing models would still fail closed. New prices or a different key mean a different ladder. Serving OpenJev needs a GPU and the nvcc/CUDA_HOME fixes we hit on the Shadeform image.

Full decisions, System One JSON traces, and CLI: docs/evidence/openjev-routing/ (ANNOUNCEMENT_FACTS.md, routing-decisions.live.json, benchmark.live.json) and docs/OPENJEV_ROUTING_DEMO.md in https://github.com/metrum-ai/genai-smart-router (or your public repo URL).

---

## Notes for you before posting

- Swap in the real public GitHub / docs URL if the path above differs.
- If you animate: attach `claude-animation-data.json` in Claude Desktop; do not invent numbers beyond this draft.
- Optional cut for length: drop the three medium-miss parentheticals; keep s03.
