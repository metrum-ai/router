# LRP evaluation splits and serving-provider variance

Operator notes for learned-routing-policy follow-ups [#31](https://github.com/metrum-ai/router/issues/31)
and [#32](https://github.com/metrum-ai/router/issues/32).
See the [learned routing policy runbook](./LEARNED_ROUTING_POLICY.md) for the full
pipeline. Caller-facing behavior remains summarized in the
[product routing guide](../docs-site/docs/routing/learned-routing-policy.md).

These features are offline evaluation aids. They do not authorize live routing
activation or promotion by themselves. Keep synthetic wiring evidence separate
from provider-backed promotion evidence.

## Near-duplicate-aware splits (#31)

Templated agent prompts can inflate holdout metrics when near-identical requests
land in both train and test through different sessions. Before assigning the
deterministic session split, `lrp featurize` optionally drops later rows whose
embedding cosine to an earlier kept row is strictly above a reviewed threshold
(default `0.98` from the #15 design). The earliest sample is kept.

```bash
lrp featurize --requests "$LRP_DATA_DIR/requests.ndjson" \
  --embedding-model "$LRP_DATA_DIR/embed/model.onnx" \
  --tokenizer "$LRP_DATA_DIR/embed/tokenizer.json" \
  --near-dup-cosine 0.98 \
  --out "$LRP_DATA_DIR/features.parquet"
```

Deduplication is opt-in. After enabling it, filter judgments and responses to the
kept `request_id` set before `train`/`eval` (training rejects outcomes that lack
a feature row). Omit `--near-dup-cosine` to keep the undeduplicated baseline.

Content-free reporting:

- Stdout includes `near_duplicate` counts: `input_rows`, `kept_rows`,
  `removed_rows`, the active threshold, and `sensitivity_removed` at `0.95`,
  `0.98`, and `0.99`.
- A protected sidecar `$out.near_dup.json` stores the same scalars next to the
  parquet artifact.
- Reports never persist prompts, tool payloads, images, credentials, token
  hashes, or full configuration.

Synthetic embeddings prove the dedup wiring and count reporting only. Semantic
near-duplicate quality for production datasets requires the operator's real ONNX
embedding artifacts.

## Serving-provider variance (#32)

Aggregator backends (for example OpenRouter `:nitro` routes) can return an
actual upstream `provider` that differs from the catalog provider string. Fanout
already records that value on `ResponseRow.serving_provider` when present.

`lrp eval` extends holdout outcomes with that metadata and emits
`serving_provider_variance` in the JSON report and Markdown summary:

- Exact catalog `provider` and `model` identity are preserved; models are never
  collapsed.
- Observed serving-provider buckets report outcome quality, stored cost means,
  and latency (`duration` / `ttfb`) percentiles.
- Missing or blank `serving_provider` is counted as missing evidence under a
  dedicated `missing` bucket; the report never invents a backend identity.
- Cross-backend variance of quality/cost/latency means is reported only across
  observed serving providers (missing evidence excluded from that variance).

Inspect `serving_provider_missing_evidence` and
`aggregator_serving_provider_variance_observed` warnings when reviewing
promotion. Synthetic fixtures may omit serving providers; that is wiring-only
and is not provider-backed variance evidence.

## Seed corpus splits (#157)

Public seed construction reuses `session_split` so each source session stays in
one train/validation/test partition. Stratification also balances turn-index
buckets (1-5, 6-20, 21+), `cl100k_base` prompt-token buckets, and reference turn
kind (tool-call or text).

Cache pass rows are repeated observations of the same turn and target. They must
not be treated as independent training examples or as an increased unique-turn
count. Report unique turns separately from replay rows.

Teacher-forced histories come from another model. Identical candidate prompts do
not remove that reference bias. Continuation agreement is not end-to-end task
success. The example seed corpus uses at most 100 source traces and N=100
sampled turns as a practical first-run budget, not statistical sufficiency.
Operators must validate coverage per target and split before promoting a bundle
built from the seed.
