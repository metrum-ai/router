<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Security event examples

The Sigma rules target OCSF JSONL records emitted by `server.security.events`. The synthetic traces exercise the authentication failure, security finding, and containment activity selections. The Go test checks each rule's selection against at least one trace.
