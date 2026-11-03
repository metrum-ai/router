---
title: Tool-aware routing case study
doc_type: explanation
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Tool-aware routing: BFCL v4 case study

This is the measured result for the tool-aware routing strategy ([#436](https://github.com/metrum-ai/router/issues/436)). It reports what happened, including the part that did not move: on this task set the router detected **no** invalid tool calls, so retry and escalation never ran and task success and cost were the same within sampling noise.

## What was measured

- **Task set:** [BFCL v4](https://gorilla.cs.berkeley.edu/blogs/15_bfcl_v4_web_search.html), released 2025-07-17, from the `gorilla` repository pinned at commit [`58f57e9124`](https://github.com/ShishirPatil/gorilla/tree/58f57e9124ea981403792dd51e00a6577e621fae/berkeley-function-call-leaderboard/bfcl_eval/data). Categories: `simple_python` (400), `multiple` (200), `parallel` (200) — 800 entries per arm, all of them; no subset.
- **Transport:** the router's OpenAI Chat surface, unary requests, `tool_choice: auto`, temperature 0.001, `max_tokens` 512. Function names with dots are rewritten to underscores, as BFCL's own OpenAI handler does.
- **Scoring:** BFCL's official AST checker from `bfcl-eval` 2026.3.23, not a local reimplementation.
- **Router:** commit [`aa6bbce1f3`](https://github.com/metrum-ai/router/commit/aa6bbce1f35e42533426d2d3eedf95516b9100b6) (the enforce-mode slice head), built from that commit, one process per run.
- **Arms:** the same two targets, `gpt-5.4-nano` (`weight: 100`) then `gpt-5.4-mini` (`weight: 1`).
  - **A `bfcl-static`:** `strategy: static`, cheapest target first.
  - **B `bfcl-tool-aware`:** `strategy: tool_aware`, `mode: enforce`, `min_samples: 20`, `window_seconds: 3600`, `validate: {arguments_json: true, schema: basic, name_must_be_offered: true}`, `recovery: {max_retries_same_target: 1, max_escalations: 1, on_exhausted: deliver_last}`.
- **Runner:** [`scripts/tool_routing_case_study.py`](https://github.com/metrum-ai/router/blob/main/scripts/tool_routing_case_study.py). It refuses to guess the model name, reads cost from the router's usage rows, and writes every failure with its BFCL error type.

## Results (2026-11-03)

| Arm | simple_python | multiple | parallel | Total | Router usage rows | Actual cost |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| A `bfcl-static` | 357/400 | 171/200 | 169/200 | **697/800** | 800 | **$0.08671945** |
| B `bfcl-tool-aware` | 361/400 | 171/200 | 169/200 | **701/800** | 800 | **$0.08652820** |

- **Detected invalid calls in arm B:** 0. Every one of the 800 attempts recorded `tool_validation.action: observed`, no failure class, and `attempts: 1`. No retry and no escalation was triggered, so the recovery path never changed a response.
- **Cost difference:** $0.00019 (0.2%) between arms, i.e. noise; recovery cost is zero because recovery did not run.
- **Success difference:** 4 entries, all in `simple_python`. The two arms are separate provider samples (BFCL is not seed-locked at the API), so a 4-entry difference is sampling noise, not an effect. The previous run of the same two-arm configuration had A=701, B=700; the ordering of the two arms reversed between runs, which is the clearest statement of the size of the noise.
- **BFCL failure types in both arms** are semantic, not structural: `value_error:string` (40 / 38), `parallel_function_checker_no_order:cannot_find_match` (18 / 17), `wrong_count` (13 / 14), `value_error:list/tuple` (6 / 6), `no_tool_call` (5 / 0). None of these is visible to argument-schema validation: the arguments are well-formed JSON that satisfies the declared schema and still names the wrong value.

## Why the result is null, and what it says

The strategy's recovery is defined on *detectable* failures: unparsable JSON, schema violations, missing required arguments, unknown tool names, a forced call that never arrives, and truncated arguments. On BFCL v4 AST categories with these two models, that set was empty: `gpt-5.4-nano` produced structurally valid calls for all 800 entries and lost points on argument *values*. Routing cannot repair a wrong value by retrying another model with the same prompt and no feedback; that is a prompt or tool-design problem, not a validation one.

So the honest conclusion is: **no measured task-success or cost effect on this task set**, because the failure mode this feature targets did not occur. The mechanism itself is covered by deterministic tests (retry on the same target, escalation with the serving target identified, ceiling and deadline bounds, discarded-usage quota settlement), not by this run. A task set where models emit malformed JSON or omit required arguments would exercise it; that set is not BFCL v4 AST categories, and choosing a set because it makes the feature look good is exactly what the release rules forbid.

## Limits

- Single-turn AST categories only. BFCL's multi-turn and live categories need tool execution and state, and were not run; no claim is made about them.
- 800 entries per arm, one provider sample each, not seed-locked: the 4-entry difference is noise. This is a single task set, not a pass rate.
- Two OpenAI models on one surface (OpenAI Chat, unary). Streaming, Anthropic Messages and cross-dialect paths are not exercised; streamed responses are observed but never retried by design in this slice.
- `tool_choice: auto`: a model that answers with text instead of a call is not a validation failure, so recovery cannot act on it.
- The reliability ordering did not engage: `min_samples: 20` with only one target accumulating outcomes keeps the configured order; only recovery could have behaved differently, and it had nothing to act on.
- Cost is the router's own `request_usage` rows at the configured target prices, not a provider invoice. The two arms total $0.17324765 for this run.

## Rerun

```sh
# one router, two arms, BFCL data pinned at the recorded revision
CASE_STUDY_TOKEN=<caller-token> python3 scripts/tool_routing_case_study.py \
  --base-url http://127.0.0.1:18136 \
  --static-group bfcl-static --tool-aware-group bfcl-tool-aware \
  --revision 58f57e9124ea981403792dd51e00a6577e621fae \
  --workers 6 --out tmp/case-study/results.json
```

The router configuration used for the run is in the artifact snapshot `25bb29cc` (`router / v47-package / toolroute-436-bfcl`), together with the raw results, the usage database, the router request log, the run log and the pinned data files.

Prepared by Metrum AI.
