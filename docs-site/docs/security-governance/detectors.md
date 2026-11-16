<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# External detectors

Router detector strategies use the versioned `metrum.detector.v1` contract at
`POST /v1/detect`. Detector services run as separate processes or services.
The router does not load Go plugins, shared libraries, WASM modules, or scripts.

Requests carry bounded metadata only: event kind, caller identifier, request
identifier, source IP, and a key prefix. Do not add prompt text, caller tokens,
provider credentials, or router memory. Responses contain detector ID,
severity, a score from 0 through 1, an optional key identifier or prefix, and
evidence request IDs. The wire decoder rejects unknown fields, malformed
signals, and oversized payloads.

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

The service returns a sample signal for `kind: example.match` and an empty
signal list for other event kinds. Replace the sample strategy with deployment
rules while keeping the wire contract.
