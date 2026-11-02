# Parallel tool calls: live gate results

Last run: 2026-11-02 (UTC), router `main` at `77a61b3e`, live gate script `scripts/parallel_tool_live.py`.

This page states exactly which parallel-tool shapes are proven, and how. Offline coverage of the same matrix (translated directions, opt-out controls, awkward SSE fragment boundaries) lives in the repository test suite; this page is only about the live provider runs.

## What the gate does

For each cell it runs a two-turn streaming conversation:

1. **Turn 1** asks for three files to be read "all at once" with a single `read_file` tool description. The gate requires at least three `tool_use` / `tool_calls` blocks, distinct non-empty ids, and complete JSON arguments containing a `path` in every call.
2. **Turn 2** returns those results **out of order**, with one error result, one multi-part text result and one deliberately empty result, then asks the model to quote the markers it received. The gate requires the two non-empty markers and an explicit `EMPTY` acknowledgement in the streamed answer.

Every dispatch is preceded by a hard budget reservation and the run stops if reported usage ever exceeds its reservation. The run is capped at $5.

## Results (2026-11-02)

| Cell | Calls in turn 1 | Turn 1 | Turn 2 | Result |
|---|---:|---:|---:|---|
| OpenAI Chat, direct (`gpt-4o-mini`) | 3 | 200 | 200 | pass |
| OpenAI Chat, through the router (`gpt-4o-mini`) | 3 | 200 | 200 | pass |
| Anthropic Messages, direct (`claude-haiku-4-5`) | 3 | 200 | 200 | pass |
| Anthropic Messages, through the router (`claude-haiku-4-5`) | 3 | 200 | 200 | pass |

Router-recorded usage for the routed cells: 95 input / 64 output tokens and $0.00005265 for Chat; 631 input / 138 output tokens and $0.001321 for Messages. The direct cells are billed by the provider outside the router, so their cost is not in the router's usage rows.

**Actual cost of this run:** $0.002681 in router usage rows (list-rate calculation at the configured prices: Chat $0.15/$0.60 per million input/output, Messages $1/$5 per million), plus the two direct calls, which the provider invoices. The budget reserved $0.3424 and never had to halt.

## Limits

- Four cells only: native Chat and native Messages, both streaming, both direct and routed. Translated directions (Chat ↔ Messages, Responses ↔ Chat, Chat/Responses ↔ Messages), non-streaming requests, and the `parallel_tool_calls: false` / `disable_parallel_tool_use` opt-outs are covered by offline HTTP tests, **not** by this live run.
- Two cheap models on one day. Provider-side interleaving of argument fragments across calls is observed only when a provider chooses to interleave; this gate does not force it.
- Turn 2 is a single reordered continuation, not a multi-round agent loop, and the final answer check is a marker echo, not a semantic judgement.
- The direct cells bypass the router entirely, so they measure provider behaviour only.
- Runs cost real money; the gate is a release-time check, not a per-PR CI job.

Rerun:

```bash
make test-parallel-tool-live   # writes .out/parallel-tool-live.json, requires provider keys
```
