---
title: Experiment Profiles (TTFT and Token Pacing)
doc_type: reference
---

# Experiment Profiles (TTFT and Token Pacing)

Experiment profiles inject a controlled **added time-to-first-token (TTFT)
delay** and **pace delivered output** for requests that match an explicit
scope. They exist for endpoint-bound agent studies (#379): measuring how
agents behave as upstream time-to-first-token rises or output delivery slows,
independently of model capability and host parallelism.

Profiles are **off by default** and must be **scoped** to experiment callers,
groups, or targets. A profile with no scope is rejected at config validation,
so experiments can never shape normal traffic. Pacing only ever *delays*
delivery — it cannot make an upstream faster than its real performance.

This is an experimental configuration surface; it is not covered by the
stable config compatibility contract.

## Configuration

```yaml
server:
  experiment_profiles:
    study-ttft-2s:
      enabled: true
      description: "sweep point: 2s added TTFT"
      seed: "study-a"               # reproducible jitter
      callers: ["bench-agent"]      # at least one of callers/groups/targets
      groups: ["agent-default"]
      targets: ["fireworks/deepseek-v4p1-flash"]
      ttft_delay_ms: 2000
      ttft_jitter_ms: 250           # uniform +/- jitter, seeded per request
    study-rate-30tok:
      enabled: true
      seed: "study-a"
      callers: ["bench-agent"]
      output_tokens_per_sec: 30     # estimated delivered-token rate (see below)
      token_estimate_chars_per_token: 4
      burst: 1
      max_extra_delay_ms: 60000
    study-events-10:
      enabled: true
      callers: ["bench-agent"]
      events_per_sec: 10            # explicitly labeled SSE-event pacing
      burst: 2
```

A request matches a profile when **every** non-empty scope list contains it:
the caller ID, the resolved model group, and the selected upstream as
`provider/model`. When several enabled profiles match, the first one in
sorted profile-name order applies, so resolution is deterministic.

## Semantics

- **Added TTFT delay** (`ttft_delay_ms`, `ttft_jitter_ms`): the router holds
  the first response byte for the computed delay. Jitter is seeded from
  `seed + profile hash + request ID`, so a sweep is reproducible per request
  and comparable across runs.
- **Delivered-token pacing** (`output_tokens_per_sec`): frames are released
  on a token schedule. The token estimate uses
  `token_estimate_chars_per_token` (default 4) and is recorded as an
  *estimate* — SSE chunks or characters are never presented as exact model
  tokens. Non-streaming responses are held for `ttft_delay` plus
  `output_tokens / rate`, using the upstream's reported usage.
- **Event pacing** (`events_per_sec`): an explicitly labeled alternative that
  paces SSE frames (events) directly, with no token claim. Exactly one of the
  two pacing modes may be set per profile.
- **Burst** releases the first N events/estimated tokens immediately.
- **`max_extra_delay_ms`** caps the total injected delay per request; shaping
  never waits past the cap.
- **Protocol fidelity:** frames pass through byte-for-byte. Tool-call deltas,
  reasoning events, usage, and `[DONE]` are unchanged. Client cancellation
  and upstream timeouts still abort a paced wait immediately.
- **Isolation:** admission, quotas, RPM/TPM, and reservation shaping are
  unchanged and apply before any pacing; profiles affect only delivery
  timing, not admission or routing. Cache hits skip pacing (the cache answers
  before upstream timing exists).

## Telemetry and provenance

Upstream timing is measured independently of pacing: the usage row keeps
`upstream_duration_ms` / upstream token rates, while `ttfb_ms` /
`downstream_duration_ms` include the injected delay. Every paced request
records request-trace events:

- `experiment_profile_applied` — profile name and profile hash (sha256 of
  the canonical profile JSON) at stream start,
- `experiment_profile_pacing` — profile name, hash, and total injected
  milliseconds at completion.

Use these to separate injected delay from real upstream behavior in the
analysis. Active concurrency, retries, and 429s remain in the existing
attempt/trace telemetry.

## Measurement tolerances

The Go tests and the e2e checks assert injected timing with a documented
tolerance of **-10% on the TTFT band** (measurement starts at request send,
so client-observed TTFT is bounded below by the delay) and **-20% on paced
stream duration** (scheduler granularity on loaded runners). Report sweeps
against an unshaped baseline on the same host.

## AgenticBench usage notes

Sweep `ttft_delay_ms` and `output_tokens_per_sec` (or `events_per_sec`)
independently at fixed task, core allocation, agent concurrency, and model
capability; keep virtual-model membership, weights, routing policy, and the
promptfoo capability vector fixed during a speed sweep. Combine with caller
traffic shaping (`request_start_per_sec`, reservations) for the queue/quota
axis — reservation shaping is admission control, not delivered-token pacing,
and is reported separately in usage rows.

Metrum AI
