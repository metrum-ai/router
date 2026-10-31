---
title: Escalation and decision-model evidence
doc_type: explanation
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Escalation and decision-model evidence

**Partial, synthetic evidence, not a production claim.** This page reports two
bounded v4.5 measurements: a three-tier escalation continuum (local CPU →
datacenter GPU → frontier API) on one AMD and one NVIDIA datacenter GPU, and
the routing accuracy and added policy time of the `d3` and `clef` decision
models on a datacenter GPU. Both use 12 hand-authored synthetic cases, so they
establish that the paths run and are measurable, not what they will do to
representative traffic.

Every number below names the committed evidence folder it comes from and the
archived snapshot that holds the raw logs, hashes and teardown records. Large
raw data is kept out of Git in the shared artifact library under
`router / v45-proof / 427` and `router / v45-proof / 428`.

## Escalation continuum

Each of 12 invented arithmetic cases ran twice through the router, once with
every request sent to the frontier model (`frontier-only`) and once with the
escalation policy choosing among the three tiers (`escalation`). Correctness is
exact match against a fixed reference answer. Local CPU and datacenter GPU
served pinned Qwen2.5 models on official vLLM images; the GPU legs were reached
over an SSH tunnel.

| Tier set | Path | Exact | Tier mix (local / GPU / frontier) | Median request | Token-rate arithmetic |
| --- | --- | ---: | --- | ---: | ---: |
| CPU + AMD MI300X + frontier | frontier-only | 12/12 | - / - / 12 | 1,311.542 ms | $0.001652 |
| CPU + AMD MI300X + frontier | escalation | 12/12 | 6 / 3 / 3 | 563.963 ms | $0.000630 |
| CPU + NVIDIA A6000 + frontier | frontier-only | 12/12 | - / - / 12 | 1,254.026 ms | $0.001482 |
| CPU + NVIDIA A6000 + frontier | escalation | 12/12 | 6 / 3 / 3 | 580.305 ms | $0.000510 |

Evidence: [AMD MI300X run](https://github.com/metrum-ai/router/tree/main/examples/escalation-continuum/evidence/mi300x-2026-10-30)
(snapshot `a371eee3`) and [NVIDIA A6000 run](https://github.com/metrum-ai/router/tree/main/examples/escalation-continuum/evidence/a6000-2026-10-30)
(snapshot `c2d3e27f`). Each folder holds the 24 raw per-request records and the
derived summary used for the columns above.

The frontier baseline made 12 hosted attempts; escalation made 3, 15 in total
per run. The case labelled private contains only invented arithmetic: the
baseline sent it to the frontier while escalation selected the local tier. The
case labelled for a 200 ms budget selected the local tier and finished in
562.864 ms on the AMD run and 531.875 ms on the NVIDIA run, so it missed that
illustrative target. These case-ID rules are not a privacy or complexity
classifier, and a latency label is a routing input, not a measured SLA.

Before either GPU run, a CPU-only preflight completed the same fixture directly
against pinned `Qwen/Qwen2.5-0.5B-Instruct` with 6 of 12 exact answers; its
failed first attempt and its offline-cache retry are archived in
`router / v45-proof / 427` snapshots `16477da4` and `ff829971`. That probe used
no router and no GPU; it qualifies the CPU model fetch, not the continuum.

### Cost of the escalation runs

| Item | Label | Amount | Evidence |
| --- | --- | ---: | --- |
| MI300X infrastructure, successful run | estimate (hours × list rate), invoice pending | $0.346313 (481.361 s × $2.59/h) | `a371eee3` |
| MI300X infrastructure, earlier failed bootstrap | estimate (hours × list rate), invoice pending | $0.096101 | `6d9f81a4` |
| A6000 infrastructure | provider post-deletion estimate, not reconciled billing | $0.0387500787 | `c2d3e27f` |
| Hosted frontier tokens, frontier-only | list-price arithmetic on reported usage, not an invoice | $0.001652 | [summary.json](https://github.com/metrum-ai/router/tree/main/examples/escalation-continuum/evidence/mi300x-2026-10-30) |
| Hosted frontier tokens, escalation | list-price arithmetic on reported usage, not an invoice | $0.000630 | [summary.json](https://github.com/metrum-ai/router/tree/main/examples/escalation-continuum/evidence/mi300x-2026-10-30) |

The MI300X figures are the v4.5 decision for a lagging provider invoice: hours
elapsed from creation to verified teardown multiplied by the quoted hourly
rate. They are **estimates with the invoice pending**, not billed charges. No
provider invoice has been reconciled for either GPU run, and the token figures
exclude CPU and GPU cost entirely, so no savings claim follows from them.

## Decision models: d3 and clef

`d3-lite` and `clef-flash` served real decisions from a dedicated NVIDIA A6000
through the router, each model receiving the same 12 hand-authored examples
nine times in shadow mode and nine times in enforce mode. Both modes recorded
108 successful decisions per model with zero policy errors. Agreement is with
the illustrative cheap/strong policy labels shipped in
[`benchmark_dataset.json`](https://github.com/metrum-ai/router/tree/main/examples/decision-models/benchmark_dataset.json),
which are preferences, not independently validated answer quality.

| Adapter | Mode | Label agreement (12 unique cases) | Confusion (actual cheap → cheap/strong, actual strong → cheap/strong) | Policy p50 / p99 |
| --- | --- | ---: | --- | ---: |
| `d3` | shadow | 9/12 | 54/0, 27/27 | 185 / 291 ms |
| `d3` | enforce | 9/12 | 54/0, 27/27 | 219 / 296 ms |
| `clef` | shadow | 12/12 | 54/0, 0/54 | 241 / 1,099 ms |
| `clef` | enforce | 12/12 | 54/0, 0/54 | 243 / 286 ms |

Evidence: [A6000 d3 and Clef observation](https://github.com/metrum-ai/router/tree/main/examples/decision-models/evidence/a6000-2026-10-29)
(snapshot `bd7a5fa8`), which holds the raw decision rows, per-mode confusion
matrices and dataset SHA-256. A first attempt on the same GPU (snapshot
`ce1db5cd`) produced real `d3` decisions but `clef` failed startup before
inference, so it contributes no Clef numbers. A fake-adapter wiring baseline of
648 requests is archived separately as snapshot `35f4e7d9`; it measures the
harness, not a model.

The two adapters are not interchangeable measurements: `d3` asks difficulty,
reasoning and task-kind questions and applies its own routing rules, while
`clef` asks one two-tier question and applies a confidence floor. Clef's 12/12
therefore reflects agreement with the label policy its question was written
against, not superior model accuracy, and the 12 labels are too few and too
artificial to rank either model. The policy times are router-recorded decision
durations that include the SSH hop to the GPU services, sequential ordering and
possible cold start: they are not co-located added latency, have no direct
baseline, and the correlated repetitions do not give an independent p99 tail.

### Cost of the decision-model runs

| Item | Label | Amount | Evidence |
| --- | --- | ---: | --- |
| A6000 first attempt | provider estimate, not reconciled billing | $0.169854118 | `ce1db5cd` |
| A6000 retry | provider post-deletion estimate, not reconciled billing | $0.2097638588 | `bd7a5fa8` |
| Both attempts, provisional sum at $0.57/h | provider estimate, not reconciled billing | $0.3796179768 | `bd7a5fa8` |

Generation in both modes pointed at a fake backend, so no hosted inference was
billed and no downstream answer quality, misroute cost or saving is measured.

## Choosing a decision model with this data

The measured evidence supports only narrow guidance:

- **Choose `d3` when decision time matters most.** It recorded the lower
  policy p50/p99 in both modes (185/291 ms shadow against Clef's 241/1,099 ms)
  while making a decision on every request in this fixture. Those times include
  the SSH hop, so treat them as an upper bound on the co-located cost and
  re-measure on your own host.
- **Choose `clef` when your policy really is a confidence-floored two-tier
  question.** It agreed with 12/12 of the labels, but that is largely by
  construction: the labels encode the same two-tier question. Do not read it as
  a higher-accuracy model.
- **Do not choose from these numbers alone.** Neither model has been scored on
  representative traffic, neither run measured the downstream quality or cost
  of a misroute, and the edge (Strix Halo class) cell is **pending, amd-halo
  offline** and deferred beyond v4.5. Run your own labelled sample.

`OpenJev` is excluded from the v4.5 comparison: its weights are CC BY-NC 4.0
and that licence has not been cleared for this work, so no OpenJev result is
published and none is implied.

## Rerun

The credential-free path runs on a laptop with Go, Python 3.12 and `uv`; it
uses loopback fakes only and makes no paid calls. From the repository root:

```sh
go build -o /tmp/router ./cmd/metrum-ai-router
uv run --with pyyaml python examples/escalation-continuum/benchmark_run.py --router /tmp/router
```

That one command reproduces the fake three-tier fixture (about 9 s). The
decision-model harness has its own single command:

```sh
go build -o /tmp/router ./cmd/metrum-ai-router
uv run --with pyyaml python examples/decision-models/run.py --router /tmp/router \
  --benchmark-dataset examples/decision-models/benchmark_dataset.json \
  --policies d3,clef --repetitions 9
```

It reproduces the labeled shadow/enforce probe with fakes in under two minutes
(measured 1 m 52 s) and prints the same agreement and confusion tables; the
fake adapters are keyword rules, so their values differ from the hardware run.
Both examples ship unit tests
(`python3 -m unittest discover -s examples/<example> -p 'test_*.py'`).

The real-hardware runs need provisioned endpoints first; nothing in the
repository creates GPUs. To reproduce the measured escalation continuum, serve
the two pinned Qwen2.5 models on loopback (CPU and GPU, official vLLM image),
set the frontier credential in the approved environment, and run:

```sh
uv run --with pyyaml python examples/escalation-continuum/benchmark_run.py \
  --router /tmp/router --backend mixed --allow-paid \
  --local-url http://127.0.0.1:8000/v1 --local-model YOUR_LOCAL_MODEL \
  --datacenter-url http://127.0.0.1:8001/v1 --datacenter-model YOUR_DC_MODEL \
  --state-dir /dev/shm/escalation-run-$(date +%s)
```

To reproduce the decision-model table, serve `d3-lite` (and a Clef-compatible
shim) on loopback and run:

```sh
uv run --with pyyaml python examples/decision-models/run.py --router /tmp/router \
  --benchmark-dataset examples/decision-models/benchmark_dataset.json \
  --policies d3,clef --repetitions 9 --decisions external \
  --d3-url http://127.0.0.1:18300 --d3-model d3-lite \
  --d3-revision YOUR_PINNED_REVISION --d3-timeout-ms 3500 \
  --clef-url http://127.0.0.1:18301/v1/systemone --clef-model clef \
  --state-dir /dev/shm/decision-private-$(date +%s)
```

Both mixed/paid forms spend money and write private router state: inspect
current pricing, use a new private absolute state directory, and delete it
after reconciling every attempt, including failures. Neither command
provisions, watches or tears down the GPU host.

## Limits

- **Synthetic data.** Twelve hand-authored arithmetic cases and twelve
  hand-authored routing labels. No representative task distribution, no
  held-out quality ground truth, no statistically useful sample size.
- **Fixture routing rules.** Tier and label choice come from case IDs, exact
  prompt matching and a character-count proxy. They are not complexity,
  sensitivity or latency classifiers and must not be used with real data.
- **Non-co-located timing.** Datacenter GPU traffic traversed an SSH tunnel, so
  every GPU-side time includes network. Median request times and policy p50/p99
  are not co-located added latency, the runs were sequential rather than
  randomized, and the AMD and NVIDIA hosts differed in hardware, vLLM build and
  network path, so no controlled vendor comparison is claimed.
- **Fake generation for the decision models.** Both tiers in the decision-model
  runs used a fake backend. Downstream answer quality, misroute quality and the
  cost of a misroute are unmeasured, so no savings or quality-parity claim
  follows.
- **Shadow and enforce were sequential.** Both modes ran on the same cases one
  after the other, not simultaneously on live traffic.
- **Costs are not invoices.** MI300X figures are estimates (hours × list rate)
  with the invoice pending; A6000 figures are provider estimates; token amounts
  are list-price arithmetic on reported usage. No provider billing has been
  reconciled, and CPU/GPU infrastructure is excluded from every token figure.
- **Two models, not three.** `OpenJev` is excluded for licence reasons, so the
  requested three-model comparison does not exist in v4.5.
- **Edge cell deferred.** The Strix Halo class edge box is **pending,
  amd-halo offline** and deferred beyond v4.5, so no edge accuracy or latency
  number is published.
- **Escalation reasons come from the fixture.** The routing-hop log records the
  fixture's own reason markers; no production escalation-reason distribution
  was measured.

Metrum AI
