---
title: Train and evaluate Learned Routing Policy
doc_type: howto
---

# Train and Evaluate Learned Routing Policy

LRP trains a routing policy, not upstream language models. Offline, LightGBM
fits per-target quality and output-token models. Quality scores are
isotonic-calibrated. Evaluation uses a session-disjoint held-out split.

Callers still request a deployment-defined model group. Training datasets,
responses, and judgments stay in operator-controlled storage.

## Pipeline

```text
approved workload dataset
  -> collect -> fanout -> verify/judge -> featurize -> train -> evaluate
  -> validated bundle
```

After `uv sync --project services/learned-routing-policy --locked`:

```bash
uv run --project services/learned-routing-policy --locked lrp collect
uv run --project services/learned-routing-policy --locked lrp fanout
uv run --project services/learned-routing-policy --locked lrp judge
uv run --project services/learned-routing-policy --locked lrp featurize
uv run --project services/learned-routing-policy --locked lrp train
uv run --project services/learned-routing-policy --locked lrp eval
uv run --project services/learned-routing-policy --locked lrp validate --bundle "$LRP_BUNDLE_DIR"
```

Each target needs at least 200 training rows to participate in learned
selection. That is a sample-count floor, not proof of workload coverage.

## GPU training hosts

Train evidence and promotion bundles on a GPU host. Skip CPU-only training for
those jobs. Any operator-owned or rented GPU system is fine. Shadeform is one
optional cloud path some operators use for development; it is not required.

When you create a cloud GPU instance for training (Shadeform or similar), create
it only when the job is ready, then terminate it when the job finishes or fails.
Do not leave idle training hosts running.

Local CI and the synthetic demo may train on CPU with synthetic fixtures. Those
runs are not hardware evidence.

Operator detail:
[LRP GPU training](https://github.com/metrum-ai/router/blob/main/docs/LRP_GPU_TRAINING.md).

## Public seed corpus limits

Seed construction tooling can replay pinned public coding trajectories through
the router. Teacher-forced continuation agreement is not end-to-end task
success. Tool mismatches are reference-agreement signals, not automatic task
failures. Coding traces do not cover unsampled chat or image workloads. Cache
passes repeat observations and do not increase unique-turn counts. N=1000 is a
budget, not sufficiency. A reference bundle is an example for specific targets
and data. Changing targets or workloads requires renewed evaluation.

## Synthetic demo

```bash
make lrp-test
make lrp-synthetic-demo
```

Synthetic results do not authorize live promotion. Provider-backed outcomes and
real embedding latency are separate evidence.

Flags, splits, and promotion gates:
[operator runbook](https://github.com/metrum-ai/router/blob/main/docs/LEARNED_ROUTING_POLICY.md).
Evaluation splits: [LRP_EVAL_SPLITS.md](https://github.com/metrum-ai/router/blob/main/docs/LRP_EVAL_SPLITS.md).
Serve and promotion: [Serve and promote](lrp-serve-and-promote.md).
