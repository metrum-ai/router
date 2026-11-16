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
      webhook_allow_hosts: [siem.example]
      webhook_secret_env: SECURITY_EVENTS_WEBHOOK_SECRET
```

The webhook key must be at least 32 bytes. Restart the router after rotating the environment variable. The HTTPS hostname must appear in `webhook_allow_hosts`; the egress client blocks private destinations and does not follow redirects. Each POST includes an RFC 3339 timestamp, a random nonce, and an HMAC-SHA256 signature in `X-Router-Timestamp`, `X-Router-Nonce`, and `X-Router-Signature`. The signature covers `timestamp + newline + nonce + newline + exact JSON body`. Receivers must verify the signature in constant time, enforce a short timestamp window, and atomically reject previously seen nonces for at least twice that window. The router retries at most once with a fresh nonce; consumers can deduplicate event delivery using the OCSF `uid`. Do not put the key in the config file.

Only authentication/authorization failures and throttles, containment actions, DLP/egress/quota findings, spend-breaker events, and detector findings enter the stream; successful request and admin-access records remain in the security report database only. Each process owns a bounded sink queue and an independent bounded webhook queue. A slow webhook cannot block stdout/file output. Sink overflow increments `security_event_dropped_total`; webhook overflow increments `security_event_webhook_dropped_total`; write and webhook errors increment `security_event_delivery_failed_total`. Queue depths are `security_event_queue_depth` and `security_event_webhook_queue_depth`. Shutdown drains each worker for up to five seconds. File output is created with owner-only permissions, appended without truncation, and reopened on the next event after a logrotate rename.

Events use OCSF `Account Change` (3001), `Authentication` (3002), `API Activity` (6003), or `Detection Finding` (2004), with `metadata.version` set to `1.4.0`. Caller suspension and release use Account Change Disable (activity 5) and Enable (activity 2), identifying the target in `user.uid` and the acting principal in `actor.user`. The stream omits prompts, request bodies, token values, and free-form reason text. Current sources include authentication failures and throttles, caller suspensions and quota exhaustion, DLP blocks/redactions, egress blocks, provider spend-breaker trips/resets, admin authentication, authorization failures and throttles, and asynchronous detector signals or failures. Detector findings include the configured detector ID and bounded signal metadata; evidence request IDs use the OCSF finding UID, capped at 256 characters.

Changing `server.security.events` through a database-backed config reload is logged as restart-required. Restart the router for event sink, webhook, or enabled-state changes to take effect.

Firewall-hold events from #891 and honeytoken-use events from #884 are follow-up integrations. Wire them into this stream when those PRs merge. Issue #885 remains open until both are complete.

Example Sigma rules and synthetic traces are in `examples/security-events/`.
