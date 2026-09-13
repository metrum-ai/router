# Routing benchmark (issue #159 harness)

Status: harness ready. No live or paid runs executed in this PR.

Configurations A/B/C are supported. Configuration D is deferred to issue #162.

## Router-added latency

- Definition: Monotonic milliseconds from router receive to outbound httptrace.WroteRequest.
- Receive event: `router_receive` at `internal/router/service.go:1318-1352`
- WroteRequest event: `router_upstream_wrote_request` at `internal/router/service.go:2209-2230`
- Join: request_trace_events rows where event=router_upstream_wrote_request; duration_ms is receive_to_wrote_request.

## Metric families

1. Overhead: LRP decisions/sec, sidecar p50/p95/p99, router-added latency,
   CPU/memory, timeouts, error rate by class; B/C deltas versus static A.
   E2E TTFB/total are recorded and flagged upstream_dependent.
2. Effectiveness: verifier rates, target mix, abstention; primary compare versus GPT-5.6-only.
3. Cost/tokens: token categories, absolute USD versus GPT-5.6, volume versus mix decomposition.

No savings percentages.

## Drivers

- Seed replay concurrency: 1, 8, 32, 128
- Harbor concurrency: 1, 4, 16 (task set + revision recorded)
- Runs per cell: 3 (median and spread)

Throughput cells use a deterministic local upstream sink and mark rows `synthetic_upstream`.
