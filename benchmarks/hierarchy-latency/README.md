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
