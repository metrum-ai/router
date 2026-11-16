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
      webhook_allow_hosts: [siem.example]
      webhook_secret_env: SECURITY_EVENTS_WEBHOOK_SECRET
```

The webhook key must be at least 32 bytes. Restart the router after rotating the environment variable. Its HTTPS hostname must appear in `webhook_allow_hosts`; egress blocks private destinations and redirects are not followed. Each POST includes an RFC 3339 timestamp and random nonce. `X-Router-Signature` is HMAC-SHA256 over `timestamp + newline + nonce + newline + exact JSON body`. Receivers should verify in constant time, enforce a timestamp window, and atomically reject reused nonces for at least twice that window. The router retries at most once with a fresh nonce; deduplicate events by OCSF `uid`. Keep the key in the named environment variable, not config.

Only authentication/authorization failures and throttles, containment actions, DLP/egress/quota findings, spend-breaker events, detector findings, firewall decisions, and honeytoken/canary use enter the stream; successful request and admin-access records stay in the report database. Sink and webhook use separate bounded queues, so a slow webhook cannot block stdout/file output. Sink overflow increments `security_event_dropped_total`; webhook overflow increments `security_event_webhook_dropped_total`; failures increment `security_event_delivery_failed_total`. Queue depths are `security_event_queue_depth` and `security_event_webhook_queue_depth`. Shutdown drains each worker for up to five seconds. File output uses owner-only permissions and append mode, and reopens after a logrotate rename on the next event.

Events use OCSF `Authentication` (3002), `API Activity` (6003), or `Detection Finding` (2004), with `metadata.version` set to `1.4.0`. The stream omits prompts, request bodies, token values, and free-form reason text. Current sources include authentication failures and throttles, caller suspensions and quota exhaustion, DLP blocks/redactions, egress blocks, provider spend-breaker trips/resets, admin authentication, authorization failures and throttles, asynchronous detector signals or failures, firewall decisions, and honeytoken/canary use. Detector findings include the configured detector ID and bounded signal metadata; evidence request IDs use the OCSF finding UID. Example Sigma rules and synthetic traces are in the source repository's `examples/security-events/` directory.

Database-backed reloads log that changes to `server.security.events` require a router restart. Firewall decisions, honeytoken use, and canary use are included when the corresponding feature is enabled.
