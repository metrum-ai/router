<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Security event examples

The Sigma rules target OCSF JSONL records emitted by `server.security.events`. Synthetic authentication, security finding, containment suspension, and containment resumption records are embedded in the Go test. The containment rule is also checked against authentication failures and unrelated API activity to prove it does not match them.

Firewall holds from #891 and honeytoken-use events from #884 remain follow-up integrations. Wire those sources into this stream when their PRs merge; #885 remains open until both integrations are complete.
