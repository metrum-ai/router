# Sanitized run evidence — 2026-10-28

`mock-output.json` records 12 router requests using three keyword-based fake
decision endpoints and fake generation. It is wiring evidence, **not model
inference or accuracy evidence**. `fireworks-output.json` records the same fake
decision adapters but with an actual hosted generation target. The two target
aliases point to the same Fireworks model and no generated answers were graded.

The hosted run had 12 distinct request IDs, 12 successful responses, 12
reported-usage attempts and no fallback. Reported usage was 456 input tokens,
zero cached input tokens and 384 output tokens. At the configured list price
of $0.30/M input, $0.006/M cached input and $1.20/M output
([source](https://fireworks.ai/models/deepseek-ai/deepseek-v4p1-flash-1017),
2026-10-28), usage-derived generation cost was **$0.0005976**. This is not a
reconciled invoice and excludes decision-server and infrastructure costs.

An earlier hosted startup collided on a local port; its database had zero
requests and zero upstream attempts. Both runs' private tmpfs state was removed.
The hosted successful run preceded a later tightening of fake Clef's input-schema
validation; that validation does not change the adapter or its decisions.
