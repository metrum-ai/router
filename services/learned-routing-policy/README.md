# Metrum Learned Routing Policy

Standalone Python 3.12+ policy service and offline training CLI for the router's
existing `strategy: external` contract. It predicts target quality and output
tokens, then recommends the cheapest eligible target meeting an operator floor.

From the repository root:

```bash
uv sync --project services/learned-routing-policy --locked
make lrp-test
make lrp-synthetic-demo
```

The synthetic demo trains real LightGBM models using explicitly synthetic
embeddings, responses and labels. It evaluates promotion gates but cannot supply
provider quality or real-ONNX latency evidence. It writes outside the repository.
Synthetic CI training on CPU is allowed only for those fixtures; do not expand
local ML. Evidence and promotion training must run on an ephemeral remote GPU
host with ample RAM and GPU; skip CPU-only and skip laptop/desktop workstation
ML for those jobs. Shadeform is an optional cloud example (create when needed,
always tear down). BYO remote GPU is fine. See
[LRP_GPU_TRAINING.md](../../docs/LRP_GPU_TRAINING.md).

See [the operator runbook](../../docs/LEARNED_ROUTING_POLICY.md) for all pipeline
commands, protected storage, approved third-party judging, local ONNX artifacts,
manual acceptance gates, shared-loopback deployment and rollback. The
[routing benchmark harness](../../docs/evidence/learned-routing-policy/routing-benchmark.md)
(issue #159) covers A/B/C overhead, effectiveness, and absolute USD schema
offline; it does not run live benchmarks. Configuration D belongs to issue #162.
The [read-only usage import](../../docs/LRP_USAGE_IMPORT.md) joins explore metadata
from SQLite/PostgreSQL usage tables without reconstructing prompts. Near-duplicate
evaluation splits and serving-provider variance reports are documented in
[LRP_EVAL_SPLITS.md](../../docs/LRP_EVAL_SPLITS.md). Wave-2 uncertainty,
Thompson sampling, drift monitoring, and cold start are documented in
[LRP_UNCERTAINTY.md](../../docs/LRP_UNCERTAINTY.md). The
[design reconciliation](../../docs/LRP_DESIGN_RECONCILIATION.md) records corrections
to issue #15 before implementation. The [caller guide](../../docs-site/docs/routing/learned-routing-policy.md)
explains request behavior.

`LRP_POLICY_AUTH_HEADER` contains the secret **value** for `X-LRP-Auth`. Blank
auth refuses startup. Serving binds loopback; admin endpoints use a separate
loopback port. Explain and reload require `--enable-admin` and authentication.
Dataset content, responses, judgments, embeddings and bundles are operator-owned
protected artifacts. Commit only small explicitly synthetic fixtures.
Optional [operator-signed bundles](../../docs/LRP_SIGNED_BUNDLES.md) add Ed25519
manifest signatures and require-signed loading with operator-owned keys. Pass
`--trust` and `--require-signed` to `lrp serve` so startup and reload enforce
the same trust policy as `lrp validate`. [Selection constraints](../../docs/LRP_SELECTION_CONSTRAINTS.md) cover
per-project floors, upstream latency gates, evidence-based cache cost estimates,
and bounded explain labels. Offline eval shares the composed decision path with
serving and records applicability when configured evidence is missing.

## Public seed corpus tooling (#157)

Iteration-1 construction helpers live in [`lrp/seed/`](lrp/seed/) with an
operator-facing note under [`seed/README.md`](seed/README.md). They audit the
pinned Nebius OpenHands trajectories source, extract teacher-forced turns,
sample session-disjoint strata, describe mixed hosted targets, wrap router
fanout for resumable replay, label verifier-class columns, and write parquet
plus a SHA-256 manifest.

Teacher-forced replay measures continuation agreement on fixed histories. It
does not prove eventual task success. Tool mismatches are agreement signals,
not automatic task failures. Coding traces do not justify quality claims for
unsampled workloads. Cold and warm cache passes are repeated observations of
the same turn. N=1000 is a practical budget, not proof of sufficiency. A
reference bundle is a first-run example, not automatic production readiness.
Paid replay stays opt-in and is not part of `make lrp-test`.
