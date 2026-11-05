<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Layered deployment reference: edge router -> datacenter router -> frontier

This is a reference configuration for the three-tier hierarchy in #434. It is a
deployment pattern, not a new product feature: it uses the shipped router as an
OpenAI-compatible downstream router with `upstream_kind: router`, `Via`-based
loop protection, per-hop usage stores and edge-local response caching.

```
            +------------------+        +---------------------+        +-----------------+
 caller --> | edge router      |  hop 1 | datacenter router   |  hop 2 | frontier APIs   |
            | (this device)    | -----> | (regional DC)       | -----> | (hosted or vLLM)|
            +------------------+        +---------------------+        +-----------------+
                 cache: on                 cache: off
                 usage DB (edge)           usage DB (dc)
```

| Tier | Role | What it proves |
|---|---|---|
| Edge | Terminates callers, enforces per-caller policy, serves repeat requests from its own cache, hands off to the DC router | Hop headers and loop protection on; nearest-safe-level caching; per-hop usage rows |
| Datacenter | Owns provider credentials and frontier targets, adds regional policy, records its own usage | Per-hop cost and latency as its own usage rows; the edge's token is authenticated here |
| Frontier | Hosted APIs or a self-hosted vLLM endpoint | The terminal hop; no further router |

## Files

| Path | Purpose |
|---|---|
| `docker-compose.yml` | edge router, datacenter router, PostgreSQL (one instance, one database per hop), usage migrations, demo frontier |
| `config/edge.yaml` | edge tier: caller auth, cache, hop limits, handoff to the DC router |
| `config/dc.yaml` | datacenter tier: accepts the edge router token, frontier targets, hop limits |
| `env.example` | every value the stack needs, including the two router tokens and the identifier key |
| `verify.sh` | one-command verification against compose (`--compose`) or local binaries (`--local`) |
| `demo-frontier/echo.py` | loopback OpenAI-Chat fixture used only for the credential-free demo |

## Run it

The fastest validation needs no container runtime:

```sh
cd deploy/reference/hierarchy
bash verify.sh --local
```

It builds the shipped binaries, generates three deployment tokens, applies the
reviewed usage migration gate to both throwaway SQLite stores, starts both tiers
and the demo frontier on loopback, and runs every check below.

### Compose deployment

```sh
cd deploy/reference/hierarchy
cp env.example .env

# 1. Identifier key (the transformer needs 64 bytes) and the Postgres password.
printf 'ROUTER_ID_TRANSFORM_KEY=%s\n' "$(openssl rand -hex 64)"
printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -base64 24)"

# 2. Three deployment tokens. Each writes a raw token file; only the hash goes
#    into .env. Never commit either.
for slug in app edge-to-dc dc-to-edge; do
  ../../bin/metrum-ai-routerctl callers generate --owner-user ops --project edge-prod \
    --env prod --key "$slug" --allow edge-default --allow dc-frontier \
    --allow edge-loop-demo --allow loop-demo --token-out "./secrets/$slug.token"
  ../../bin/metrum-ai-routerctl callers hash --token-file "./secrets/$slug.token"
done
```

Paste the `token_hash` and `token_id` from the second command into the matching
`EDGE_CALLER_*`, `EDGE_LOOPBACK_*` and `DC_CALLER_*` pairs in `.env`
(`dc-to-edge` is the edge's loopback caller, `edge-to-dc` is the datacenter's
caller, `app` is the application caller). Set `DC_ROUTER_TOKEN` and
`DC_LOOPBACK_TOKEN` to the raw `edge-to-dc` and `dc-to-edge` token values.

```sh
# 3. Start Postgres, then complete the reviewed migration gate for both hops.
docker compose --env-file .env up -d postgres
for env in EDGE_USAGE_DSN DC_USAGE_DSN; do
  docker compose --env-file .env run --rm --no-deps \
    --entrypoint /app/bin/metrum-ai-router-migrate migrate \
    --action=apply --driver=postgres --dsn-env="$env" --scope=usage
  docker compose --env-file .env run --rm --no-deps \
    --entrypoint /app/bin/metrum-ai-router-migrate migrate \
    --action=resume --job=historical-usage-validation-v1 --checkpoint-ordinal=0 \
    --driver=postgres --dsn-env="$env" --scope=usage
  docker compose --env-file .env run --rm --no-deps \
    --entrypoint /app/bin/metrum-ai-router-migrate migrate \
    --action=verify-serving --driver=postgres --dsn-env="$env" --scope=usage
done

# 4. Start both tiers and verify.
docker compose --env-file .env up -d
ACCESS_TOKEN="$(cat ./secrets/app.token)" bash verify.sh --compose
```

The routers use `migration_policy: deployment-job`, so they refuse to serve
while the gate is pending. That is the intended production posture: a router
that has not passed the reviewed migration gate fails closed.

By default the datacenter tier sends to the `frontier-echo` demo service, which
returns fixed OpenAI-Chat responses and records the headers it received, so the
stack needs no provider credentials. Point it at a real frontier API or a
self-hosted vLLM endpoint by setting `FRONTIER_BASE_URL`, `FRONTIER_API_KEY` and
`FRONTIER_MODEL` in `.env`; no config change is needed.

`verify.sh` checks:

1. `/readyz` on both tiers and `/v1/models` for the caller's allowed groups.
2. A streamed chat through both hops returns 200 and the frontier received
   exactly two `metrum-ai-router` `Via` entries and no client-supplied entry.
3. A request loop between the tiers ends as HTTP 508 `upstream-loop-detected`,
   not an unbounded ping-pong.
4. Two identical unary requests (`temperature: 0`) with a frontier-call count:
   the edge answers the second from its cache.
5. Each hop wrote its own usage row for the same work, with per-hop request ID,
   latency, tokens and cost; the script prints both rows side by side.
6. The request identifiers at the two hops are printed, so the correlation limit
   below is visible rather than assumed away.

## What each hop does

**Hop headers and loop protection.** Both tiers set `server.upstream.max_router_hops`
(4 here). Each router appends one `Via: <version> metrum-ai-router` entry,
forwards only sanitized router entries, and answers 508 when the limit is
reached. The guard is best effort: it depends on intermediaries preserving
`Via`, and it is not an authorization mechanism. The edge strips third-party
`Via` entries so a caller cannot smuggle one across the hop.

**Per-hop cost and latency.** Each tier writes its own usage rows: status,
latency, TTFB, upstream duration, tokens and cost. There is no folded
cross-tier total, because a hop cannot see the other hop's price book. To get a
total, sum the two stores over the same window and caller; `verify.sh` prints
both rows for one request. A hop that fronts a self-hosted model has a token
cost of zero unless the deployment sets a cost-allocation price.

**Caching at the nearest safe level.** Only the edge enables the response cache,
and it is caller-scoped and deterministic: identical requests from the same
caller within `default_ttl` are served without a DC call. The DC tier leaves the
cache off so a repeated request cannot be answered from a second, divergent
cache layer. Enable the cache only where a stale response is acceptable for the
workload; it is not a semantic cache.

**Request identifiers across hops.** This is a real limitation, not configuration.
Every router mints its own `X-Request-Id`, so a caller sees the edge's ID and the
DC's row carries a different ID. Correlate by caller token, model group and
timestamp window today; the edge's `X-Request-Id` is not forwarded as the DC's
identifier. [#434](https://github.com/metrum-ai/router/issues/434) decided
**A, reference config only**: layered deployment ships as this reference
configuration plus the release hierarchy matrix, and a propagated hop
correlation identifier is not a v4.7.0 product feature. `verify.sh` prints both
identifiers and does not pretend they match. Shared hierarchy governance is
tracked for v5.0.0.

**Timeouts.** Client timeout > edge budget > DC budget > longest frontier
attempt. Both configs set a 600 s upstream timeout, which is a ceiling for
long coding-agent turns; a real deployment should set per-tier budgets that
narrow toward the frontier.

**Credentials.** The edge holds a datacenter router token (`DC_ROUTER_TOKEN`);
the datacenter holds the frontier keys (`FRONTIER_API_KEY`). Neither tier can
read the other's credential. The DC validates the edge token as an ordinary
caller, so its usage rows are attributable to the edge hop.

## Evidence and limits

- The topology's latency and TTFT are measured by
  `benchmarks/hierarchy-latency/` (#587), which starts the same two routers in
  front of a self-hosted backend and reports direct vs 1-hop vs 2-hop p50/p99.
- The compatibility of chained routers is covered by the release hierarchy
  matrix (#434), which runs real agent clients through depths 0, 1, 2, 3 and 5.
- This reference config does not claim cross-hop request-ID correlation, a
  folded cross-tier cost total, shared cache invalidation, or that the demo
  frontier is a model. It also does not replace the tier-specific reference
  configurations for AMD/NVIDIA/Intel hardware.

## Validated tiers (2026-11-03)

The reference config has been run with real inference backends, both on
dedicated nodes (one process per tier on its own node, plus the verification
procedure above) and on an edge-class Strix Halo box. Dedicated-node evidence:
`router / v47-package / 587`, snapshot `bb4aed02`; the Halo run's raw report is
archived by the run operator.

| Tier | Hardware | Serving stack | Result |
|---|---|---|---|
| Datacenter (AMD) | AMD Instinct MI325X (SKU M3250101, 274.5 GB VRAM), 20 vCPU | official `rocm/vllm@sha256:e0ea9a10b729573ea8598bdc073bda0ea382fe1037eec7c0761b6fa7f46baaf4`, vLLM 0.29.1, `Qwen/Qwen2.5-1.5B-Instruct` | two-hop chain 200; per-hop additions +2.33 ms TTFT p50 (one hop) and +3.12 ms (two hops); per-hop usage rows for every hop; loop protection 508 |
| Datacenter (Intel CPU) | Intel Xeon Gold 6548N, 16 vCPU | official `vllm/vllm-openai-cpu@sha256:87167cf0ec6e1e9607bb1cf9bf618a67d04963236a34bd955ac75b7d31a6edf5`, vLLM 0.33.0, `Qwen/Qwen2.5-0.5B-Instruct` | same topology; two-hop chain 200; TTFT p50 268.8 ms direct, 274.8 ms two hops |
| Edge (Strix Halo) | Strix Halo edge-class box; edge and DC tiers both on that box | the bundle vLLM primary reached directly by the DC tier, edge and DC configs unchanged | 300/300 requests at 0, 1 and 2 router hops; TTFT p50 139.90 / 142.66 / 143.16 ms and p95 142.34 / 145.09 / 145.22 ms (details and caveats below) |

`bash verify.sh --local` passed all checks on a dedicated node (hop-header
hygiene, loop 508, edge cache hit, both hops' usage rows, env/config drift
check). The compose path uses the same configs and the same script; it was not
exercised in the measurement window because the run used one process per tier
directly.

### Edge-tier run (Strix Halo)

A separate run measured the same topology on an edge-class Strix Halo box, with
router `main` at `006d0397` and the bundle's vLLM primary reached directly by the
datacenter tier (edge and DC configs unchanged). Each lane sent 300 requests.

| Router hops in front of the backend | TTFT p50 | TTFT p95 |
|---|---:|---:|
| direct (0 hops) | 139.90 ms | 142.34 ms |
| one hop (edge) | 142.66 ms | 145.09 ms |
| two hops (edge → DC) | 143.16 ms | 145.22 ms |

One hop adds **2.76 ms** at p50 and 2.75 ms at p95; two hops add **3.26 ms** at
p50 and 2.88 ms at p95 in this run. Every request succeeded (300/300 per lane).

Read this row with its caveats:

- **Both the edge and the datacenter tier ran on the same Strix Halo box.** The
  hop cost is real router work on edge-class hardware, but it is not a
  distributed deployment and it is **not** a real datacenter claim.
- The full `verify.sh` procedure above was not run on that box; the hop-header,
  loop-protection, cache and per-hop-usage checks come from the dedicated-node
  run, not from this one.
- A separate three-hop bundle-group result is part of the Halo run's own report;
  it is not reproduced here.

What this does **not** establish: the cross-hop request-ID correlation and folded
cost total above are still absent, no run has exercised a hosted frontier API,
and the datacenter tier has not been measured on its own separate hardware in
this configuration.
