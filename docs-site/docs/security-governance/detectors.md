---
title: External Detectors
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# External detectors

Router detector strategies use the versioned `metrum.detector.v1` contract at
`POST /v1/detect`. Detector services run as separate processes or services.
The router does not load Go plugins, shared libraries, WASM modules, or scripts.

Requests carry bounded metadata, including an opaque key ID. Prompts, user
messages, caller tokens, provider credentials, and router memory are never
sent. By default no text is sent. An operator may set a detector's optional
`include_segments` list to `tool_result`, `document`, or both. Only text from
those selected segment kinds is sent to that detector's allowlisted endpoint;
the router warns at config load when this is enabled. Segment text is not
logged, stored, measured, or copied into result signals. Signals contain only
metadata such as score, severity, segment kind/index, request ID, and opaque
key ID. The wire decoder rejects unknown fields, malformed signals, and
oversized payloads.

The external detector transport requires HTTPS and an exact `allow_hosts`
entry. Loopback HTTP is available for local development. Each redirect is
checked again against the configured host list; URL userinfo is rejected. The
request deadline bounds slow or hung services. Detector policies
support `off`, `report`, and `enforce` modes, a timeout from 1 ms to 5 seconds.
A configured detector defaults to `report`; set `mode: off` to disable it
explicitly. Policies also support
`sample_rate` from greater than 0 through 1, and `on_error: fail_closed` or
`fallback`. Enforce results are delivered asynchronously through the
`SetDetectorResultConsumer` interface for containment consumers; they never
change the request that already completed. Queue overflow drops detector work
and increments the Prometheus counter `metrum_ai_router_detector_dropped_total`
so serving does not wait for capacity. The router queue is process-local.
Example rolling rule state is held by the detector service process, resets
when that service restarts, and is not shared by multiple detector service
instances. Point all router replicas at the same detector instance when they
must contribute to one fleet-wide baseline.
Detector results enter the security access event store when
`server.admin_reports.security.enabled` is true.

Example configuration:

```yaml
server:
  detectors:
    - id: local-rules
      url: https://detector.example/v1/detect
      allow_hosts: [detector.example]
      mode: report
      timeout_ms: 500
      on_error: fallback
      sample_rate: 1
      include_segments: []
```

Run the example service:

```sh
go run ./examples/external-detector
```

Run the local contract suite:

```sh
DETECTOR_URL=http://127.0.0.1:8081/v1/detect go test ./pkg/detector -run TestExternalContract
```

The example service runs four bounded rogue-agent rules:

| Rule | Default trigger |
| --- | --- |
| `auth_fail_storm` | 25 authentication failures per opaque key ID or source IP in one minute |
| `validation_sweep` | 20 requests with a body of at most 256 bytes per key in one minute |
| `model_enumeration` | probes for five distinct denied model groups per key in five minutes |
| `spend_spike` | current-minute token use reaches six times the mean of at least five observed baseline minutes in the prior 15 minutes |

Signals include a severity, rule ID, opaque key identifier, score, and bounded
evidence request IDs. They do not include prompts, tokens, or secrets. A new
key produces no spend-spike signal until it has five observed baseline minutes;
missing minutes do not count toward that baseline.

For the key-based auth-failure rule and the other key-scoped rules, the signal
key is the router-provided opaque event key ID. The source-IP auth-failure
signal has an empty key because it is scoped to an address, not a caller key.
The rules keep at most 10,000 LRU keys per rule and 256 observations per key;
key-based and source-IP auth-failure state use separate LRUs, so a flood of new
IP addresses cannot evict key state. Each event window is bounded to 15 minutes.
Observations newer than evaluation time and observations outside the rule window
do not contribute to a signal. Rule thresholds are configurable
with `--auth-failures`, `--tiny-requests`, `--denied-groups`, and
`--spend-multiple`; defaults are 25, 20, 5, and 6 respectively.

Rule state is bounded and local to the example detector process. Multiple
example detector instances do not share state, and restarting the service
clears its baseline.
The example service supports TLS with `--tls-cert` and `--tls-key`.

## Automatic containment

Detector signals can be mapped to temporary key throttles or suspensions. The
default mode is `off`; `shadow` writes proposed actions to the security event
path without affecting requests, and `enforce` applies matching rules. Only
signals from detectors configured with `mode: enforce` are eligible. The
per-minute action cap and exempt caller IDs apply before every write. Every
action expires at its configured TTL.

```yaml
server:
  auto_containment:
    mode: shadow
    max_actions_per_minute: 5
    exempt_callers: [emergency]
    rules:
      - detector_id: honeytoken
        severity: critical
        action: suspend
        ttl_ms: 300000
      - detector_id: rogue_agent
        severity: high
        action: throttle
        throttle_percent: 10
        ttl_ms: 60000
    unlimited_throttle_limit: 100
```

`unlimited_throttle_limit` is required when a rule throttles callers whose
configured limit is unlimited (`0`); it supplies the base limit before the
percentage is applied. Throttling remains in the usage database. Run
`router-migrate` before enabling this feature. Postgres replicas receive a
dedicated NOTIFY signal and poll shared throttle rows every second as fallback;
SQLite applies throttle state locally.

An operator can release an automatic throttle before its TTL by adding the
caller ID to `server.auto_containment.exempt_callers` and applying the config
reload. The runtime throttle is removed on refresh while its stored action
record remains until expiry; keep the caller exempt until then to prevent the
active record from being applied again.

An enforced `suspend` rule requires `server.config_source.mode: database` and
uses the caller-containment path. Its canonical record is marked as
containment-made by the detector, so the containment role or an administrator
can resume it before its TTL. Shadow mode writes only proposed actions and does
not affect admission. The router records detector ID, caller ID, action, and
expiry metadata. It never copies token values or prompt text into containment
state or events.
