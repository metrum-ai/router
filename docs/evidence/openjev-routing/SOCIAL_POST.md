# LinkedIn draft (OpenJev external routing) — under LinkedIn ~3k char limit

Paste below. Target ≤2900 characters. Facts from the 2026-09-25 live Shadeform run.

---

Most LLMs are built to write. Routing and agent control usually need a fast, typed *decision* with probabilities you can threshold.

That is the bet behind **Jev** (TypeSafe’s System One model): declare the shape up front — choice among your labels, calibrated yes/no, or ordered score — and get structured answers software can use directly. No paragraph to parse. No inventing an option that was not on the menu.

**OpenJev** is the open-weights cousin of that idea (independent of TypeSafe; not their hosted product). Same shape: state + typed questions (`choice` / `score` / `noul`). Probabilities, not prose. Serve it yourself. Weights CC BY-NC 4.0.

Metrum AI Router’s routing layer is pluggable. Last week: LRP (learned on your traffic). This week: an external policy that asks OpenJev which *task class* a request is, then maps it onto priced OpenAI targets. OpenJev does not write the answer — the router still owns eligibility, upstream, usage, latency, and fallback.

We served `openjev/openjev` on Shadeform `RTXPro6000` (RTX PRO 6000 Blackwell, 97887 MiB, vLLM 0.29.0 FP8). Live ladder (key had `gpt-6-astra`, not gpt-6-luna/sol):

- simple → `gpt-5.6-luna` ($0.20 / $1.20 per 1M)
- medium → `gpt-5.6-sol` ($4 / $20)
- advanced → `gpt-6-astra` ($10 / $50)

60 labelled prompts × 2: **56/60 (93.3%)** label accuracy. OpenJev p50 **193 ms**. Repeat flips: **0**. Escalations: 4.

Examples:
- “Summarize this product update…” → simple 0.995 → `gpt-5.6-luna` (186 ms)
- “Implement a Python function that merges two sorted lists…” → medium 0.999 → `gpt-5.6-sol` (194 ms)
- “Prove… consistency and latency for a global ledger…” → advanced 0.996 → `gpt-6-astra` (195 ms)
- Honest miss: billing-dispute classify still chose simple, but noul 0.972 → escalate to `gpt-5.6-sol`

Start in `shadow`, promote to `enforce`. N=60 is a fixture, not production traffic. Receipts + runbook: https://github.com/metrum-ai/router (`docs/evidence/openjev-routing/`, `docs/OPENJEV_ROUTING_DEMO.md`).

---

## Notes

- If LinkedIn still rejects: drop the three “Examples” clean hits; keep the miss line.
- Full long form stays in git history / `ANNOUNCEMENT_FACTS.md`.
