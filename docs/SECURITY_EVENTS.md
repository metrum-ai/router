<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Security event stream

The opt-in `server.security.events` stream writes sanitized security records as JSON Lines using the pinned OCSF 1.4.0 schema. It supports stdout, an append-only file, and an optional HTTPS webhook. The existing security report database remains independent.

```yaml
server:
  security:
    events:
      enabled: true
      version: "1.4.0"
      output: file
      path: /var/log/router/security-events.jsonl
      webhook_url: https://siem.example/events
      webhook_secret_env: SECURITY_EVENTS_WEBHOOK_SECRET
```

The webhook key must be at least 32 bytes. Each POST includes an RFC 3339 timestamp and an HMAC-SHA256 signature in `X-Router-Timestamp` and `X-Router-Signature`. The signature covers the timestamp, a newline, and the exact JSON body. The router retries once; the webhook must tolerate duplicate events. Do not put the key in the config file.

Each router process owns its own bounded in-memory queue and writer; events are emitted by the process handling the event, and queued events are not durable across a process crash. The writer never waits for a sink in the request path. Queue overflow increments `security_event_dropped_total`; write and webhook failures increment `security_event_delivery_failed_total`. Queue depth is exposed as `security_event_queue_depth`. On shutdown, queued records drain for up to five seconds. File output is created with owner-only permissions and appended without truncation.

Events use OCSF `Authentication` (3002), `API Activity` (6003), or `Detection Finding` (2004), with `metadata.version` set to `1.4.0`. The stream omits prompts, request bodies, token values, and free-form reason text. Current sources include authentication failures and throttles, caller suspensions and quota exhaustion, DLP blocks/redactions, egress blocks, provider spend-breaker trips/resets, admin API authentication failures/throttles, and asynchronous detector signals or failures. Detector findings include the detector ID and bounded signal metadata; evidence request IDs use the OCSF finding UID.

Example Sigma rules and synthetic traces are in `examples/security-events/`.
