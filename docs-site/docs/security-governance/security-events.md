---
title: Security Event Stream
doc_type: reference
---

# Security Event Stream

The opt-in `server.security.events` stream writes sanitized security records as JSON Lines using OCSF 1.4.0. Choose stdout or append-only file output, with an optional HTTPS webhook. The existing security report database remains independent.

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

The bounded writer never waits for a sink in the request path. Queue overflow increments `security_event_dropped_total`; write and webhook failures increment `security_event_delivery_failed_total`. Queue depth is exposed as `security_event_queue_depth`. On shutdown, queued records drain for up to five seconds. File output uses owner-only permissions and append mode.

Events use OCSF `Authentication` (3002), `API Activity` (6003), or `Detection Finding` (2004), with `metadata.version` set to `1.4.0`. The stream omits prompts, request bodies, token values, and free-form reason text. Current sources include authentication failures and throttles, caller suspensions and quota exhaustion, DLP blocks/redactions, egress blocks, provider spend-breaker trips/resets, and admin API authentication failures/throttles. Detector-specific sources are added with their detector implementation. Example Sigma rules and synthetic traces are in the source repository's `examples/security-events/` directory.
