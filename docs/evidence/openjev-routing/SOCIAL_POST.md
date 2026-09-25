# Social post draft (from live OpenJev run)

A 27B model that writes nothing chose cheap summarization vs GPT-6 Astra — in ~193 ms.

Measured on Shadeform **RTX PRO 6000 Blackwell** with real OpenJev weights + Metrum AI Router external policy:

- simple → `gpt-5.6-luna` ($0.20 / $1.20)
- medium → `gpt-5.6-sol` ($4 / $20)
- advanced → `gpt-6-astra` ($10 / $50)

Live labelled suite: **56/60 (93.3%)** task-class matches · **0** flips on repeat · OpenJev p50 **193 ms**.

Cost sample (15 OpenAI calls): always-Astra projection ~$0.30 vs routed ~$0.15 on the same mean-cost model.

Reproduce: `docs/OPENJEV_ROUTING_DEMO.md` · evidence: `docs/evidence/openjev-routing/benchmark.live.json`

Notes: OpenJev weights CC BY-NC 4.0 · OpenJev ≠ hosted Jev · not 100% — we show the 4 misses too.
