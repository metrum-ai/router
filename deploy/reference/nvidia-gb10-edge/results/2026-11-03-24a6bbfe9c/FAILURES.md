<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Failures and limits of the GB10 check

| # | Item | Result |
|---|---|---|
| 1 | `local-chat` slot (Qwen3-30B-A3B) on GB10 | **not proven.** One host downloaded the checkpoint and initialised the engine, but the API server never came up within 15 minutes; one host had no egress at all. |
| 2 | Co-located deployment | **not possible on this provider.** Vast GPU instances are unprivileged containers, so the tier's `compose.yaml` cannot run there; the router ran on the operator host and only the model-server base URL differs from the committed config. |
| 3 | Concurrency / latency | **not measured.** The load driver requires the direct lane on loopback; there is no loopback path across the internet. |
| 4 | `local-tiny` on GB10 | pass: the pinned arm64 image served it and the tier's group answered through the router. |
