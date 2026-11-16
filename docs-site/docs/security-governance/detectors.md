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
support `off`, `report`, and `enforce` modes, a timeout from 1 ms to 5 seconds,
`sample_rate` from greater than 0 through 1, and `on_error: fail_closed` or
`fallback`. Enforce results are delivered asynchronously through the
`SetDetectorResultConsumer` interface for containment consumers; they never
change the request that already completed. Queue overflow drops detector work
and increments the Prometheus counter `metrum_ai_router_detector_dropped_total`
so serving does not wait for capacity. Queue and rolling rule state are per
router process; they reset at restart and are not shared across replicas.
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

The example service runs the bounded rogue-agent rules for authentication
storms, tiny-request sweeps, denied model-group enumeration, and spend spikes.
It supports TLS with `--tls-cert` and `--tls-key`.
