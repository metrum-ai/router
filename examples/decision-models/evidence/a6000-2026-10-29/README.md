<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# #428 A6000 retry — real d3 and Clef-flash, incomplete comparison

A dedicated NVIDIA RTX A6000, 49,140 MiB, served d3-lite revision
`03560c26f5199c3c04129a2ccb38960c913635e0` via the pinned official vLLM
image, then Clef-flash revision `f0b7d3cd65296abfe459e6c228b618f5d8b26d2f`
via pinned official SGLang. Clef started with `--mem-fraction-static 0.85`,
`--max-running-requests 8` and context length 4096; identity endpoint reported
`Cloudflare/clef-flash`. Service identity and image/GPU metadata are included.

Each model received 12 original synthetic examples, repeated nine times in
shadow and nine times in enforce. Both produced 108 successful decisions and
zero recorded policy errors in each mode. The **illustrative policy-label
agreement**, not independently validated answer quality, was:

| adapter | mode | unique example agreement | repeated confusion (actual cheap→cheap/strong, actual strong→cheap/strong) |
|---|---|---:|---|
| d3 | shadow | 9/12 | 54/0, 27/27 |
| d3 | enforce | 9/12 | 54/0, 27/27 |
| clef | shadow | 12/12 | 54/0, 0/54 |
| clef | enforce | 12/12 | 54/0, 0/54 |

The d3 adapter asks difficulty, reasoning and task-kind questions and applies
its own routing rules; the Clef adapter asks a different two-tier question and
applies a confidence floor. This does not isolate model weights or justify a
model recommendation. Twelve labels are hand-authored policy preferences,
not a held-out representative benchmark. Repetitions are correlated. The
shadow and enforce runs used the same cases sequentially, not simultaneously.

Successful router policy-duration p50/p99 (ms) was d3 shadow 185/291, d3
enforce 219/296, Clef shadow 241/1099 and Clef enforce 243/286. The router and
adapters ran on the controller over an SSH tunnel to GPU services: these
numbers include network and adapter time, sequential ordering and possible
cold-start effects. They are not co-located added latency, have no direct
baseline, and the repeated observations do not provide independent p99 tails.
Both generation tiers used the same **fake** backend; no downstream answer
quality, misroute costs, savings or hosted inference is measured.

OpenJev is excluded from the v4.5 comparison because its weights are
CC BY-NC 4.0. Edge: **pending, amd-halo offline**, deferred beyond v4.5.
The d3/Clef datacenter observation remains limited as described above.
`d3-result.json`, `clef-result.json`, and `benchmark_dataset.json` retain the
synthetic rows, outcomes and confusion matrices.

The independent watcher verified the VM and its registered public key absent
and removed the local key. This retry's deleted-instance provider estimate was
$0.2097638588; combined with the first attempt's $0.169854118 the provisional
sum is $0.3796179768, not reconciled billing. Quoted rate $0.57/hour.
