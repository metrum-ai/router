# Social post draft (OpenJev routing demo)

A 27B model that writes nothing chose cheap summarization vs GPT-6 Astra.

Metrum AI Router + OpenJev as an external routing engine:

- simple → gpt-5.6-luna ($0.20 / $1.20 per 1M)
- medium → gpt-5.6-sol ($4 / $20)
- advanced → gpt-6-astra ($10 / $50)

Measured on this repo's labelled fixture (fake OpenJev shim for CI wiring):
60/60 task-class matches. Live OpenAI smoke on 6 prompts: ~$0.01 total,
Astra alone ~170x Luna on that sample's token bill.

Reproduce:

```bash
make openjev-routing-demo
python3 scripts/openjev_routing_benchmark.py
```

Shadeform RTXPro6000 runbook for real OpenJev weights:
`docs/OPENJEV_ROUTING_DEMO.md`

Notes for posting:
- OpenJev weights are CC BY-NC 4.0 (research / non-commercial unless licensed).
- OpenJev ≠ TypeSafe hosted Jev.
- Do not claim GPU OpenJev accuracy until you run the Shadeform path and replace the fake shim.
- Link commit SHA from `docs/evidence/openjev-routing/benchmark.json`.
