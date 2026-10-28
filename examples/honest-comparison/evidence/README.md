# Synthetic comparison evidence (2026-10-28)

`mock-output.json` is one local run of the actual Metrum router with the length
policy versus `llm-router-lite` 0.1.0 with explicitly configured cost and
quality scores. Both sent the same six labelled prompts to the same mock
backend. In this run the proxy passed 4/6 and the library passed 5/6; the
library alone selected the stronger mock on H2. Both failed H1. This outcome
follows the artificial tier labels and mock backend, not measured LLM quality.

The per-arm median client latencies were 18.506 ms and 16.337 ms respectively,
including backend HTTP round trips. The library ran in-process while Metrum
used a local proxy plus a separate policy service; these are not like-for-like
component overhead measurements. Six samples, local scheduling and synthetic
backend timings are insufficient for a general performance claim.

One prior mock run failed with HTTP 502 because its two-tier config left a
three-tier policy threshold unchanged; no paid provider was contacted. It was
excluded from the completed comparison. Local mock compute cost is not measured.

`fireworks-output.json` records the same two routing implementations using the
same hosted model (DeepSeek V4.1 Flash) for both tier aliases. Six simple
exact-answer prompts yielded 6/6 per arm; this is not evidence that routing
improves quality. There were 12 distinct request IDs, 12 successful requests
and no retries or fallback; all usage was reported. Metrum used 336 input and
139 output tokens ($0.0002676), and the library arm used 336 input and 171
output tokens ($0.0003060); cached input was zero. At the configured dated
list prices ($0.30/M input, $0.006/M cached, $1.20/M output;
[Fireworks source](https://fireworks.ai/models/deepseek-ai/deepseek-v4p1-flash-1017),
2026-10-28), total usage-derived cost is **$0.0005736**, not a reconciled
provider invoice. Router DB records and direct-provider receipts were checked;
private tmpfs state was removed.

Hosted medians are end-to-end request latency with different network topologies
and provider variability, not standalone routing overhead. A local-backend
option was added after this run; no actual self-hosted model was tested.
