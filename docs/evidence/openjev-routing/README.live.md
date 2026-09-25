# OpenJev live routing evidence

Generated: `2026-09-25T18:50:32.714039+00:00`
Commit: `ee179afa31ae87306a3d9373402bb6a754717759`
Hardware: `Shadeform massedcompute RTXPro6000; NVIDIA RTX PRO 6000 Blackwell Server Edition 97887 MiB; driver 580.126.09; vLLM 0.29.0 fp8; CUDA_HOME=/usr/local/cuda-13.0`

## Routing (real OpenJev)

- cases: **60**
- label accuracy: **93.3%**
- OpenJev latency p50: **193 ms**
- policy e2e p50: **194 ms**
- tier mix: `{'gpt-5.6-luna': 19, 'gpt-5.6-sol': 18, 'gpt-6-astra': 23}`
- repeat flips: **0** / 60 (repeat=2)

## Cost projection (from live OpenAI samples)

- sample total USD: **0.035115** (15/15 ok)
- projected: `{'always_cheap': 0.00114, 'always_medium': 0.11928, 'always_advanced': 0.30096, 'openjev_routed_est': 0.151513}`

