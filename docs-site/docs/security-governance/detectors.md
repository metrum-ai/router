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

Requests carry bounded metadata only: event kind, caller and request IDs,
source IP, non-secret key prefix, denied group, tiny-request flag, and token
count. The token field is a numeric usage count, never a token value. Do not add prompt
text, caller credentials, provider keys, or router memory. Responses contain
detector ID, severity, a score from 0 through 1, an optional key identifier or
prefix, and evidence request IDs. The wire decoder rejects unknown fields,
malformed signals, and oversized payloads.

The external detector transport requires HTTPS and an exact `allow_hosts`
entry. Loopback HTTP is available for local development. Each redirect is
checked again against the configured host list; URL userinfo is rejected. The
request deadline bounds slow or hung services; caller cancellation cancels an
in-flight call. Detector policies
support `off`, `report`, and `enforce` modes, a timeout from 1 ms to 5 seconds,
`sample_rate` from 0 through 1, and `on_error: report` or `ignore`. Queue
overflow drops detector work and increments the detector drop counter so
serving does not wait for capacity.

Run the example service:

```sh
go run ./examples/external-detector
```

Run the local contract suite:

```sh
DETECTOR_URL=http://127.0.0.1:8081/v1/detect go test ./pkg/detector -run TestExternalContract
```

The service includes four report-only rules: `auth_failure` counts 25
authentication failures per
minute per source IP or key prefix; 20 tiny requests per minute per key; five
distinct denied groups in five minutes per key; and usage in one minute at
least six times the average active-minute spend over the prior 15 minutes.
Spend detection stays silent until the key has five active baseline minutes.
The service keeps bounded in-memory history, so a restart resets baselines and
new keys begin cold. All router replicas must send events to the same detector
service for fleet-wide counts.
