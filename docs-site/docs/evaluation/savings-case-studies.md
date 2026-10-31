---
title: Savings Case Studies
doc_type: explanation
---

# Savings Case Studies: Routed Against All-Frontier

Three small, self-contained workloads compare a router-routed model group with
an all-frontier baseline on the same tasks. Each study reports scored quality,
the request mix the router chose, latency, and what the run actually cost. The
sources, run scripts, configuration template and raw evidence live in
[`examples/case-studies/`](https://github.com/metrum-ai/router/tree/main/examples/case-studies).

These are smoke-scale proofs that the mechanism works and that cost can be
measured per arm. They are not proof of parity on your workload: the samples
are three to five tasks per arm, and one study lost quality. Read
[Limits](#limits) before quoting a number.

## What was measured

| Study | Workload | Quality signal | Tasks per arm |
| --- | --- | --- | ---: |
| [Coding agent](https://github.com/metrum-ai/router/tree/main/examples/case-studies/coding-agent) | Agent writes a calculator program from a spec, may revise once after test feedback | Independent judge runs 20 arithmetic and invalid-input cases per task outside the candidate sandbox | 3 |
| [Support agent](https://github.com/metrum-ai/router/tree/main/examples/case-studies/support-agent) | Multi-turn tool loop: look up order, calculate policy credit, apply it, open ticket | Deterministic checker requires the four-step chain, no tool errors and the correct final JSON | 3 |
| [Document Q&A](https://github.com/metrum-ai/router/tree/main/examples/case-studies/document-qa) | Lexical retrieval over five snippets, then an answer | Exact match on both retrieved document and reference answer | 5 |

Both arms see identical tasks, prompts, token caps and routing groups. The
baseline sends every request to the `frontier` group; the routed arm uses the
weighted cheap/frontier group, so the only variable is model selection.

## Results, 2026-10-28

| Study | Arm | Quality | Wilson 95% CI | Requests | Request mix | Cost | p50 / p95 latency |
| --- | --- | --- | --- | ---: | --- | ---: | --- |
| Coding agent | All frontier | 3/3 tasks, 60/60 tests | 43.9%–100% | 3 | Sol 3/3 | $0.036304 billed | 16731 / 18335 ms |
| Coding agent | Routed | **2/3 tasks, 40/60 tests** | 20.8%–93.9% | 4 | Flash 2/4, Sol 2/4 | $0.027638 (see below) | 11364 / 19127 ms |
| Support agent | All frontier | 3/3 | 43.9%–100% | 15 | Sol 15/15 | $0.013722 billed | 1488 / 1740 ms |
| Support agent | Routed | 3/3 | 43.9%–100% | 15 | Flash 8/15, Sol 7/15 | $0.007861 (see below) | 1201 / 2290 ms |
| Document Q&A | All frontier | 5/5 | 56.6%–100% | 5 | Sol 5/5 | $0.001050 billed | 1261 / 1503 ms |
| Document Q&A | Routed | 5/5 | 56.6%–100% | 5 | Flash 3/5, Sol 2/5 | $0.000611 (see below) | 1053 / 1864 ms |

"Sol" is `gpt-6.1-sol`, "Flash" is `deepseek-v4p1-flash-1017`. Request latency
is client-observed and excludes the coding sandbox and judge execution.

### Quality verdict

- **Coding agent: the routed arm lost quality.** It passed 2 of 3 tasks against
  3 of 3, with one paired loss and no wins; on the failed task the final
  candidate passed 0 of its 20 tests. The sample is far too small to estimate
  a general gap, but there is no parity claim to make here and none is made.
- **Support agent and document Q&A: no observed difference** (3/3 vs 3/3 and
  5/5 vs 5/5, no paired wins or losses). The confidence intervals are wide and
  consistent with a real difference in either direction, so this is
  "no regression observed", not "parity established".

Cost and quality did not move together: the study with the quality loss also
produced the smallest percentage saving (23.9%), while the two studies without
an observed loss saved 41.8% and 42.7%. Three to five tasks per arm cannot rank
workloads or predict which one will lose quality; route per workload and verify,
as described in [Prove Router Quality](./prove-router-quality).

## Cost: what is billed and what is estimated

The provider charges for the `gpt-6.1-sol` requests in **both** arms were
reconciled against provider per-minute usage and billed line-item rates. Request
counts and input/output token totals matched the router's usage rows exactly for
every study window, which is what makes the charges attributable to an arm even
though both arms shared one credential. The billed rates were $2.00/M input and
$10.00/M output, uncached, and the line items reconcile to the cent with the
request-time router rows.

The routed arms also sent requests to Fireworks. No per-request billing export
is available for those requests, so their amounts are **usage-derived list-rate
estimates** and are never presented as billed charges.

| Study | Frontier (billed) | Routed, OpenAI part (billed) | Routed, Fireworks part | Routed total | Reduction |
| --- | ---: | ---: | ---: | ---: | ---: |
| Coding agent | $0.036304 | $0.022616 | $0.005022 estimate | $0.027638 | 23.9% |
| Support agent | $0.013722 | $0.005652 | $0.002209 estimate | $0.007861 | 42.7% |
| Document Q&A | $0.001050 | $0.000434 | $0.000177 estimate | $0.000611 | 41.8% |

Every reduction figure includes the Fireworks estimate in the routed total.
Comparing only the requests both arms sent to the same provider gives 37.7%,
58.8% and 58.7% respectively; that narrower figure has fully billed evidence on
both sides but ignores the Fireworks spend the routed arm really incurred.

Prices are the request-time catalog rates dated 2026-10-28 in each study's
`evidence/usage.json`. The provider export was pulled on 2026-10-31; the method
and per-arm token totals are in
[`billing-reconciliation-2026-10-28.md`](https://github.com/metrum-ai/router/blob/main/examples/case-studies/common/evidence/billing-reconciliation-2026-10-28.md).
The coding-agent first attempt, which stopped early and is kept as incomplete
evidence, cost an additional **$0.037204** and is excluded from the paired
comparison.

## Evidence

Each study directory holds a README with its method, a key-free
`evidence/provenance.json` (router revision and binary hash, source hashes,
window, sandbox details where relevant), `evidence/results.json` (per-trial
outcomes), `evidence/usage.json` (every request: model, tokens, dated rates,
router-recorded cost, price source) and `evidence/summary.json` (the aggregate
numbers above). The router configuration without caller tokens is in
`common/evidence/router-config-sanitized.yaml`.

## Rerunning a study

The hosted rerun of any study is three commands from the repository root: build
the private state, build the router, then run the workload and extract evidence.
For example, document Q&A:

```sh
uv run --with pyyaml python examples/case-studies/common/setup.py \
  --config examples/case-studies/common/config.example.json --state-dir /dev/shm/case423
go build -o /tmp/case423-router ./cmd/metrum-ai-router
/tmp/case423-router --config /dev/shm/case423/config.yaml

python3 examples/case-studies/document-qa/run.py \
  --url http://127.0.0.1:18123 --token-file /dev/shm/case423/caller \
  --output /tmp/document-results.jsonl
python3 examples/case-studies/common/extract.py --workload document-qa \
  --results /tmp/document-results.jsonl --database /dev/shm/case423/usage.sqlite \
  --out-dir /tmp/document-evidence
```

The hosted commands need approved provider credentials in `OPENAI_API_KEY` and
`FIREWORKS_API_KEY`, cost real money, and must not be run to overwrite the
committed evidence directories. The support agent and coding agent use the same
sequence with their own `run.py`; the coding agent additionally needs a
disposable Docker sandbox reachable over SSH, described in its README.

Without credentials, the rerun can be verified end to end. Every command below
was run on a clean clone on 2026-10-31 and takes seconds:

| Check | Command | Measured |
| --- | --- | --- |
| Private state and router start | `setup.py` with placeholder keys, then `metrum-ai-routerctl config validate` and `/readyz` | under 5 s, config valid, `readyz` ok |
| Router build | `go build -o /tmp/case-router ./cmd/metrum-ai-router` | 31 s cold module download, 1 s warm |
| Shared evidence and cost tests | `uv run --with pyyaml python -m unittest discover -s examples/case-studies/common -p 'test_*.py' -q` | 15 tests, 10–11 s |
| Coding-agent mock runner | `python3 -m unittest discover -s examples/case-studies/coding-agent -p 'test_*.py' -q` | 4 tests, 1 s |
| Support-agent mock runner | `python3 -m unittest discover -s examples/case-studies/support-agent -p 'test_*.py' -q` | 5 tests, 1 s |
| Document Q&A mock runner | `python3 -m unittest discover -s examples/case-studies/document-qa -p 'test_*.py' -q` | 2 tests, 1 s |
| Full mock loop through a real router | `uv run --with pyyaml python examples/case-studies/common/weekly.py --router /tmp/case-router --out-dir /tmp/case-weekly-mock --mock` | 3 s, labelled `MOCK ONLY` |

The mock runs are credential-free, call no provider, and label their cost
figures as non-billed. They prove the scripts run and the evidence pipeline
produces a summary; they are not study results. The shared suite needs PyYAML,
which the commands above install through `uv`.

## Limits

- **Sample size.** Three to five tasks per arm cannot establish parity, and the
  Wilson intervals in the table are wide enough to contain large differences in
  either direction. Treat quality as "no regression observed except where one
  was", not as a measured equivalence.
- **One quality loss.** The coding-agent study lost one task. Cost reduction
  there does not come with quality held.
- **Partial billing evidence.** Only the `gpt-6.1-sol` charges are reconciled
  provider charges. The Fireworks share of each routed arm is a list-rate
  estimate; no invoice confirms it.
- **Attribution by minute and arm.** The exact match ties provider usage to an
  arm over the study window, not to an individual request. Per-request amounts
  come from the router's own usage rows.
- **Sequential arms, fixed fixtures.** The runners execute the frontier arm and
  then the routed arm; provider drift and warm-up are not controlled. Routing is
  weighted and requests are not randomized.
- **Not production workloads.** All three are self-authored synthetic fixtures.
  They measure that routing and metering work, not what your traffic will do.
- **Environment.** All figures come from one machine, one router revision and
  one model catalog snapshot dated 2026-10-28. All three studies report the same
  router binary hash, built from revision `2ecfd440` with an uncommitted worktree
  change outside the Go sources; `evidence/provenance.json` records that flag and
  the binary hash for each run. Re-verify prices before re-running; the scheduled
  weekly check refuses price dates older than 31 days.

## Reproduction status

The workflow that reruns the document Q&A study weekly is merged and enabled
for this repository; its first scheduled result and the required independent
external reproduction are still outstanding, and the external reproduction gate
has moved to the next release. Until those exist, this page is the authors'
own evidence, not an independently reproduced result.

Metrum AI
