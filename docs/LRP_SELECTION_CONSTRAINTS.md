# LRP selection constraints (Wave 2)

Operator guide for latency gates, per-project quality floors, evidence-based
prompt-cache cost estimates, and bounded explanation labels in Learned Routing
Policy selection. Companion to [LEARNED_ROUTING_POLICY.md](LEARNED_ROUTING_POLICY.md).
Implements issues
[#26](https://github.com/metrum-ai/router/issues/26),
[#27](https://github.com/metrum-ai/router/issues/27),
[#35](https://github.com/metrum-ai/router/issues/35), and
[#36](https://github.com/metrum-ai/router/issues/36).

No live routing activation is authorized by these features. Use generic fixtures
and deployment-defined project names only.

## Quality floors with per-project overrides

Each group keeps a default `quality_floor`. Optional `floors_by_project` maps
`caller.project` strings to floors in `[0, 1]`:

```yaml
groups:
  workload-staging:
    quality_floor: 0.80
    floors_by_project:
      demo-strict: 0.90
      demo-loose: 0.70
```

Resolution:

1. Strip the caller project string.
2. If it matches a configured key, use that floor.
3. Otherwise use the group `quality_floor` (empty and unknown projects included).

Validation rejects empty keys, keys longer than 256 characters, and floors
outside `[0, 1]`. Do not hardcode production project identities in sample config
or tests; author deployment-owned names in operator-protected config.

Authenticated `/explain` returns `floor` (effective), `group_floor`, and
`project_floor_override`. Use `floor_sweep_values` / offline eval floor sweeps
to compare default and project overrides before promotion. Offline `lrp eval`
carries caller project when present in the feature frame (`caller_project`) so
per-project floors participate in the same composed decision path as serving.
If floors are configured but project evidence is absent, evaluation marks
applicability unsupported and promotion cannot pass for that configuration.

## Upstream latency constraint

Optional per-group gate on stored upstream latency evidence:

```yaml
groups:
  workload-staging:
    latency_p95_ms_max: 2500   # null/omit disables
    latency_metric: duration   # or ttfb
    unknown_latency: allow     # allow | exclude
    latency_evidence:
      synthetic/fast-model: 400
      synthetic/slow-model: 8000
```

Behavior:

- When `latency_p95_ms_max` is set, targets whose observed p95 exceeds the cap
  are excluded before the cheapest-above-floor argmin.
- Evidence sources (merged per request):
  - Static `latency_evidence` seeds from offline eval (`provider/model` keys).
  - Rolling in-memory samples from authenticated `POST /feedback` (`latencyMs`
    as duration, optional `ttfbMs`), which supersede static seeds for the same
    identity.
- **Cold start / unknown latency:** if no evidence exists for a target,
  `unknown_latency: allow` (default) keeps it eligible; `exclude` drops it.
  When every known target is filtered out, selection falls back to highest
  predicted quality among available predictions and labels
  `lrp:slow` / `lrp:slowunk` (bounded).

Evaluate **upstream** p95 (duration/TTFB from feedback or eval) separately from
**downstream** caller latency (router + LRP deadline). Raising LRP `deadline_ms`
does not relax this upstream gate.

`/explain` includes `latency_p95_ms_max` and per-target `latency_p95_ms` when the
gate is enabled.

## Evidence-based prompt-cache cost estimates

Selection-time `est_cost` may use a catalog `cachedInputPricePerMillionUsd` only
when trustworthy cache metadata is present:

| Condition | Cost estimate |
|---|---|
| `promptCacheState` absent/unknown | Full `inputPrice` for all input tokens |
| Session pin alone / large `systemChars` alone | Full price (pins ≠ cache hits) |
| Explicit `hit`/`warm` **and** cached catalog price | Cached input price (or partitioned tokens) |
| Explicit `miss`/`cold` | Full input price |
| Hit without cached catalog price | Full input price (no invented savings) |
| Both `cachedInputTokens` and `uncachedInputTokens` with positive cached | Treat as hit; blend prices when catalog cached price exists |

Historical stored costs in usage/response rows are never rewritten by these
estimates. Unknown cache state must not invent savings. Router payloads may omit
cache fields today; behavior is a no-op until operators expose trustworthy
metadata.

## Bounded explanation labels

`classLabel` stays within the router charset `[A-Za-z0-9_.:-]` and ≤64
characters. Learned decisions encode a coarse kind plus optional quality/cost
buckets, for example `lrp:caf:q0.90:c2`:

- Kind abbreviations: `caf`, `floor`, `noprice`, `pinned`, `noknown`,
  `slow`, `slowunk`.
- Quality is rounded to 0.05 steps; cost uses order-of-magnitude buckets.
- No model names, project IDs, prompts, or exact floats.
- Exploration keeps the exact label `lrp:explore` (no q/c suffix) so
  usage-import and CLI filters continue to match.

Prometheus `lrp_route_total` uses only the coarse `lrp:<kind>` via `metric_label`
so cardinality stays bounded. Authenticated `/explain` continues to return exact
predicted quality, estimated cost, feature importances, floor, and cache/latency
scalars.

Degraded paths (`lrp:no-request-content`, `lrp:image-passthrough`,
`lrp:latency-fallback`, `lrp:embedding-fallback`) keep their existing labels.

## Validation

```bash
make lrp-test
```

Synthetic unit/API tests cover project floors, latency cold-start/exclusion,
cache unknown/hit/miss, and label charset/cardinality. They do not establish
provider-backed quality or production latency. Keep live activation behind the
usual shadow → staging → request-shape promotion gates.

## Caller-facing summary

See the Docusaurus page
[Selection constraints](../docs-site/docs/routing/lrp-selection-constraints.md)
for what callers observe. Contact [contact@metrum.ai](mailto:contact@metrum.ai)
for deployment questions.
