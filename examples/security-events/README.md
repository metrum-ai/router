<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Security event examples

The Sigma rules target OCSF JSONL records emitted by `server.security.events`. Synthetic authentication, security finding, honeytoken, canary, containment suspension, and containment resumption records are embedded in the Go test. Honeytoken and canary use are Detection Findings with Critical severity. Their evidence includes the caller ID in `evidences[].actor.user.uid` and source address in `evidences[].src_endpoint.ip`; no token value is emitted. The containment rule is also checked against authentication failures and unrelated API activity to prove it does not match them.

Firewall decisions, honeytoken use, and canary use are included in the stream when those features are enabled.
