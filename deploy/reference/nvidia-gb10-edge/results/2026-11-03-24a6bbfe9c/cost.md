<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Cost of this check

Four Vast GB10 containers, all destroyed:

| Instance | Host | Window (UTC) | Rate | Result |
|---|---|---|---|---|
| 58896532 | Poland, PL | 00:37:50-00:52 | $0.415/h | vLLM loaded the image and the Qwen3-30B-A3B engine; API server never came up |
| 58897127 | Zhejiang, CN | 00:57:10-01:09 | $0.245/h | no outbound egress: Hugging Face unreachable |
| 58897188 | Sweden, SE | ~01:00-01:08 | ~$0.27/h | did not produce a running server |
| 58899308 | Washington, US | 01:11:08-01:19 | $0.565/h | served `local-tiny`; functional suite 6 passed, 0 failed, 3 skipped |

**estimate (hours x list rate), invoice pending: about $0.25 total.**

The Vast billing API is not reachable for a per-instance invoice line here, so this
is labelled as an estimate.
