<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Per-hop router latency and TTFT (#587)

Measures streaming time to first token (TTFT) and total latency through zero,
one and two router hops in front of one OpenAI-Chat backend:

```
direct   client -> backend
1-hop    client -> edge router -> backend
2-hop    client -> edge router -> datacenter router -> backend
```

The routers are the shipped `metrum-ai-router` binary with the same hop headers,
loop protection and usage recording the layered deployment uses
(`deploy/reference/hierarchy/`). Every lane sends the same prompt and output cap
to the same backend, so the per-hop delta is comparable within a run.

## Run it

```sh
# Procedure dry run: loopback fixture backend (validates the lanes and arithmetic)
make hierarchy-latency

# Self-hosted backend (the only valid source for latency claims)
python3 benchmarks/hierarchy-latency/run.py \
  --backend-url http://<backend-host>:8000/v1 \
  --backend-key-env VLLM_API_KEY \
  --model <served-model> \
  --requests 400 --concurrency 8 --max-tokens 32 --name vllm-mx325
```

Run the load generator on a node separate from every router and from the
backend. Record the CPU model and core count of each node in the run notes; the
report records the load-generator host automatically.

### Multi-node run

The routers must run on their own nodes, not on the load generator. Render the
two router configs (with real node addresses and a shared state directory) and
the deployment tokens, copy them to the router nodes, start both routers, then
measure:

```sh
# 1. On the load generator: render configs addressed to the router nodes.
python3 benchmarks/hierarchy-latency/run.py \
  --bootstrap ./bootstrap \
  --edge-url http://<edge-host>:8080 --dc-url http://<dc-host>:8080 \
  --backend-url http://<backend-host>:8000/v1 --model <served-model> \
  --state-dir /var/lib/router
# bootstrap/manifest.json names the two configs and the app token file.

# 2. On each router node: mkdir -p /var/lib/router, copy its config and start
#    the shipped binary (-config <file>); it binds 0.0.0.0:8080.

# 3. On the load generator: measure against the running routers.
HIER_APP_TOKEN="$(cat bootstrap/hop-app.token)" \
python3 benchmarks/hierarchy-latency/run.py \
  --edge-url http://<edge-host>:8080 --dc-url http://<dc-host>:8080 \
  --backend-url http://<backend-host>:8000/v1 --model <served-model> \
  --edge-usage ./edge-usage.sqlite --dc-usage ./dc-usage.sqlite \
  --requests 400 --concurrency 8 --max-tokens 32 --name multi-node
```

`--edge-usage`/`--dc-usage` are copies of each router's SQLite usage file taken
after the run, so the per-hop rows in the report come from the routers, not from
the load generator. The backend token, if any, is read from the environment
variable named by `--backend-key-env`.

Output: `report.json` and `RESULT.md` under `.out/hierarchy-latency/<utc>-<name>/`.
No hosted model calls; the cost is the operator's rental.

## What is measured

| Field | Definition |
|---|---|
| TTFT | request write to the first `data:` event on the response stream (client wall clock) |
| total (streaming) | request write to the last byte of the stream |
| unary total | request write to the full response body |
| per-hop delta | lane percentile minus the direct lane percentile for the same run |
| per-hop usage | each router's own `request_usage` row for its lane: latency, TTFB, upstream duration, tokens |

The report also prints the `Via` header the backend actually received, and the
status of a deliberate loop between the two routers (expected 508), so hop
headers and loop protection are observed rather than assumed.

## Limits

- This is client-side wall clock; it includes the load generator, the network to
  the edge and every hop's queueing. It is not a router-internal profile. Use
  `benchmarks/overhead/` for CPU/heap profiles under load.
- A loopback-mock run validates the procedure only. Its absolute numbers are
  fixture latency and must never be reported as product latency.
- Only self-hosted backends are valid evidence; a hosted API's own TTFT and
  routing make a per-hop comparison meaningless.
- The lanes share one backend and one prompt. A run says nothing about
  cross-dialect translation cost, tool calls, or long prompts.

## Measured: self-hosted vLLM on AMD MI325X (2026-11-03)

- Harness commit `58db613e2a`; router binaries built from that commit.
- Backend: official `rocm/vllm@sha256:e0ea9a10b729573ea8598bdc073bda0ea382fe1037eec7c0761b6fa7f46baaf4`
  (vLLM 0.29.1) serving `Qwen/Qwen2.5-1.5B-Instruct` (served as `qwen25-1.5b`,
  `max_model_len` 4096) on an AMD Instinct MI325X (SKU M3250101, 274.5 GB VRAM).
- Nodes: GPU + one dedicated node per router hop (Intel Xeon Platinum 8358,
  16 vCPU), load generator on its own node (same CPU, 8 vCPU). Different hosts
  for backend, edge, datacenter and load generator; nothing on loopback.
- Workload: 300 streaming + 300 unary requests per lane, concurrency 8,
  `max_tokens` 32, 24 warm-up requests discarded per lane.
- Raw reports, per-hop usage SQLite copies and the environment manifest:
  `router / v47-package / 587`, snapshot `bb4aed02`.

| Lane | TTFT p50 | TTFT p99 | Stream total p50 | Stream total p99 | Unary p50 | Unary p99 |
|---|---:|---:|---:|---:|---:|---:|
| direct | 9.61 ms | 13.41 ms | 71.79 ms | 100.73 ms | 71.99 ms | 100.94 ms |
| 1-hop | 11.95 ms | 15.60 ms | 74.52 ms | 104.51 ms | 76.23 ms | 106.12 ms |
| 2-hop | 12.73 ms | 17.12 ms | 77.97 ms | 113.10 ms | 78.97 ms | 110.47 ms |

| Added by | TTFT p50 | TTFT p99 | Stream total p50 | Stream total p99 |
|---|---:|---:|---:|---:|
| one hop | +2.33 ms | +2.19 ms | +2.73 ms | +3.78 ms |
| two hops | +3.12 ms | +3.71 ms | +6.17 ms | +12.37 ms |

Per-hop usage rows for the same run (each router's own store): edge 1-hop lane
latency p50 73 ms / TTFB p50 10 ms; edge 2-hop lane 76 ms / 12 ms; datacenter
2-hop lane 76 ms / 10 ms; about 66,000 settled tokens per hop. Loop protection
answered 508 for the deliberate tier cycle.

A cold first run of the same command showed a single 1.2 s outlier on the direct
lane (vLLM's first post-startup scheduling of the workload), which dominates a
p99 from 200 samples. The table above is the warm run; discard warm-up and run
at least a few hundred samples per lane before quoting p99.

**Intel Xeon CPU tier** (same procedure, `vllm/vllm-openai-cpu@sha256:87167cf0ec6e1e9607bb1cf9bf618a67d04963236a34bd955ac75b7d31a6edf5`,
vLLM 0.33.0, `Qwen/Qwen2.5-0.5B-Instruct` on a 16-vCPU Xeon Gold 6548N, 60
requests per lane at concurrency 4): TTFT p50 268.8 ms direct, 271.5 ms one hop,
274.8 ms two hops; stream total p50 about 2.05 s in every lane. Hop overhead is
+2.7 ms (1-hop) and +6.0 ms (2-hop) TTFT p50, under 2% of the CPU backend's
TTFT. This is an indicative CPU-tier figure, not a GPU latency result.

## Limits of this measurement

- Client-side wall clock on a dedicated load-generator node; it includes that
  host, the VPC network and every hop's queueing.
- Single backend, single model, one prompt and one output cap; concurrency 8.
  It says nothing about cross-dialect translation, tool calls, long prompts or
  much higher concurrency.
- The edge and datacenter routers ran as one process each on dedicated nodes with
  the same configs the reference deployment uses; they were not containerized on
  those nodes.
