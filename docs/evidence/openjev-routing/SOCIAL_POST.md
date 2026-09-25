# Announcement source pack (distill from this)

Do not post this whole file. Distill a short social post from the exact facts in
[`ANNOUNCEMENT_FACTS.md`](ANNOUNCEMENT_FACTS.md) and the machine table
[`routing-decisions.live.json`](routing-decisions.live.json).

## One-liner candidates (all measured)

- OpenJev on an RTX PRO 6000 Blackwell classified 60 prompts in ~193 ms p50 and routed them to `gpt-5.6-luna` / `gpt-5.6-sol` / `gpt-6-astra` with **56/60** fixture matches and **0** repeat flips.
- Example: “Summarize this product update…” → OpenJev `simple` (conf 0.993, probs 0.997/0.002/0.000) → `gpt-5.6-luna` in 186 ms.
- Example: “Implement a Python function that merges two sorted lists…” → `medium` (0.998) → `gpt-5.6-sol` in 194 ms.
- Example: “Prove the multi-constraint trade-offs…” → `advanced` (0.994) → `gpt-6-astra` in 195 ms.
- Honest miss: “I was charged twice for order 4412” → OpenJev chose `simple` but `noul=0.97` → policy escalated to `gpt-5.6-sol`.

## Where the receipts are

| artifact | what |
|---|---|
| `ANNOUNCEMENT_FACTS.md` | every prompt + decision + 4 full System One JSON traces + OpenAI sample rows |
| `routing-decisions.live.json` | machine-readable 60-row table |
| `benchmark.live.json` | aggregates |
| `CLAUDE_OPUS_ANIMATION.md` | **paste-ready Opus 5.5 animation prompt** + how Claude animates (HTML/JS, not video) |
| `claude-animation-data.json` | compact measured scenes for that prompt |
| `ANIMATION_AGENT_PROMPT.md` | longer animation brief (any agent) |

Reproduce: `docs/OPENJEV_ROUTING_DEMO.md`.
