# OpenJev routing evidence

Generated: `2026-09-25T18:29:49.987793+00:00`
Commit: `e081bcec5839e2e244b2a57c1268ccea01bc5343`

## Ladder (live key)

| tier | model | $/1M in | $/1M out |
|---|---|---:|---:|
| cheap | gpt-5.6-luna | 0.20 | 1.20 |
| medium | gpt-5.6-sol | 4.00 | 20.00 |
| advanced | gpt-6-astra | 10.00 | 50.00 |

## Synthetic routing (fake OpenJev)

- cases: **60**
- label accuracy: **100.0%**
- decision latency p50: **1 ms**
- tier mix: `{'gpt-5.6-luna': 20, 'gpt-5.6-sol': 20, 'gpt-6-astra': 20}`

This is wiring evidence with a deterministic shim. For GPU OpenJev, use
`scripts/openjev_shadeform.py` and point `OPENJEV_URL` at the tunneled shim.

## License

OpenJev weights are CC BY-NC 4.0. This repository example code is Apache-2.0.

## Live OpenAI cost sample

- requests ok: 6/6
- total cost USD: **0.009656**
- by model: `{'gpt-5.6-luna': {'n': 2, 'avg_latency_ms': 2145, 'cost_usd': 4e-05}, 'gpt-5.6-sol': {'n': 2, 'avg_latency_ms': 2521, 'cost_usd': 0.002756}, 'gpt-6-astra': {'n': 2, 'avg_latency_ms': 3822, 'cost_usd': 0.00686}}`

