# LRP uncertainty, exploration, drift, and cold start

Follow-up to issues
[#24](https://github.com/metrum-ai/router/issues/24),
[#25](https://github.com/metrum-ai/router/issues/25),
[#28](https://github.com/metrum-ai/router/issues/28), and
[#30](https://github.com/metrum-ai/router/issues/30).
Wave-2 modules extend Learned Routing Policy (LRP) with calibrated uncertainty,
Thompson sampling, drift monitoring, and evidence-backed cold start. Defaults
preserve Wave-1 epsilon-greedy behavior. **No live routing activation** is
authorized by these features alone.

## Uncertainty-aware abstention (#24)

`lrp train --ensemble-size 5` (reviewed default) fits a five-member bootstrap
ensemble of quality models per target and stores member boosters under
`ensemble/` in the bundle. At serve time, when `uncertainty_abstention: true`,
LRP estimates calibrated quality standard deviation across members inside the
same bounded admission and remaining-deadline path as primary inference. Feature
build runs once per request. If the would-be primary exceeds
`uncertainty_threshold` (reviewed default `0.15`), selection abstains to
`abstention_anchor` (or the first eligible fallback) and emits the bounded label
`lrp:uncertain`.

When abstention is enabled but ensemble evidence is unavailable or insufficient
(missing artifacts, load errors, zero/one finite members, or inference errors),
selection fails closed to the same conservative anchor path with
`lrp:uncertain-unavailable`. Missing evidence is never treated as confidence.
`/readyz` reports `uncertainty: unavailable` in that degraded state while the
bundle remains loaded. Failed reloads that cannot load a required ensemble keep
the previous complete bundle and ensemble snapshot.

Operator comparison of abstention versus v1 enforce is a **safe scalar** delta
(cost, quality, latency). Unit tests cover the comparison helper with synthetic
wiring only.

## Thompson sampling (#25)

Set `exploration_strategy: thompson` and keep exploration restricted to
`exploration_projects` (approved calibration traffic). Thompson uses a single
exploration gate per request with probability `explore_rate`. When that draw
fires, LRP samples `q ~ Normal(mean, std)` from ensemble uncertainty and runs
the same cheapest-above-floor policy, labeling `lrp:explore-thompson`. A rejected
Thompson draw reaches deterministic exploitation without a second epsilon draw.
Missing Thompson estimates also skip epsilon and exploit (aligned with abstention
fail-closed). Ordinary `epsilon_greedy` is unchanged when selected.

Seeded epsilon-greedy (`exploration_strategy: epsilon_greedy`, the default)
remains available for A/B comparison. `compare_exploration_strategies` in
`lrp.thompson` produces reproducible synthetic selection-rate reports.

## Drift monitoring and reversible shadow (#28)

```bash
lrp drift-check \
  --reference-features "$LRP_DATA_DIR/features.train.parquet" \
  --observed-features "$LRP_DATA_DIR/features.recent.parquet" \
  --out "$LRP_DATA_DIR/drift-report.json" \
  --psi-threshold 0.25 \
  --embedding-distance-threshold 0.15 \
  --auto-shadow
```

The job computes Population Stability Index (PSI) on scalar features and mean
cosine distance of new embeddings to the training centroid. Finite observations
outside the reference quantile support are counted in explicit underflow and
overflow bins so mass is not discarded. A changed constant reference feature
produces drift evidence rather than unconditional zero. Outputs are bounded
safe metrics only (no prompts or raw embeddings in the report body beyond the
optional centroid export helper).

`--auto-shadow` sets `recommend_shadow` when thresholds are exceeded. LRP does
**not** mutate the router: activation remains the router's
`external_policy.mode` authority. Rollback is config-only (clear auto-shadow /
return the group to enforce after review). The policy may emit
`lrp:shadow-drift` as a label-only signal when an operator wires the
recommendation into serve-side state; Wave-1 shadow still returns the real
recommendation so the router can own serving.

## Evidence-backed cold start (#30)

When a target has at least **200** train-split anchor-comparison judgments but
cannot meet the normal training minimum (for example missing validation rows),
`lrp train` records a `cold_start: true` skipped entry with `bt_strength` and
`n_anchor_prompts`. No learned quality model is written.

With `cold_start_exploration: true`, serve may inject Bradley-Terry baseline
predictions **only for targets the router already marked eligible**, then run
Thompson-only exploration labeled `lrp:explore-cold-start` until
`n_train >= min_train_rows`. Cold start never overrides eligibility and never
activates a target without exact validation.

## Group config knobs

```yaml
groups:
  lrp-demo:
    quality_floor: 0.80
    explore_rate: 0.0
    exploration_projects: []
    exploration_strategy: epsilon_greedy  # or thompson
    uncertainty_abstention: false
    uncertainty_threshold: 0.15
    abstention_anchor: null
    auto_shadow_on_drift: false
    psi_threshold: 0.25
    embedding_drift_threshold: 0.15
    cold_start_exploration: false
    min_train_rows: 200
    mode: enforce
```

## Evidence boundary

- **Synthetic wiring:** unit tests under
  `services/learned-routing-policy/tests/unit/test_uncertainty_explore.py`
  and `tests/unit/test_issue_158.py` cover abstention labels, fail-closed
  unavailable uncertainty, Thompson single-gate rates, PSI overflow/constant
  features, auto-shadow recommendation, cold-start seed/injection, deadline-bounded
  uncertainty, and signed serve/reload wiring.
- **Provider-backed evidence** stays in the operator boundary and is never
  committed. No live routing activation is authorized by this document.
  Shadeform and other paid hardware acceptance for LRP latency is tracked in
  issue #162; this document does not claim hardware benchmarks.

See the [operator runbook](LEARNED_ROUTING_POLICY.md) for the full offline
pipeline and [usage import](LRP_USAGE_IMPORT.md) for explore metadata recovery.
