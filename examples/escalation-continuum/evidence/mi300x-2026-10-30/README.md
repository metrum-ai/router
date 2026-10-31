<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Synthetic CPU / AMD MI300X / frontier measurement

On 2026-10-30, the mixed benchmark ran each of 12 synthetic arithmetic cases
once on frontier-only and once on escalation. It used real local CPU,
MI300X and hosted frontier inference through the router—not mocks. The local
and GPU Qwen2.5 models and vLLM image were pinned; GPU traffic traversed an SSH
tunnel. See [result.json](result.json) for the 24 raw records and
[summary.json](summary.json) for derived descriptive values.

Both paths got 12/12 exact responses. Escalation selected CPU for six, MI300X
for three and frontier for three; the frontier-only baseline made 12 hosted
attempts, escalation three. The case labelled private contains only invented
arithmetic: the baseline sent that synthetic input to the frontier, whereas
escalation selected CPU. The fixture's case-ID policy is not a privacy or
complexity classifier. The case labelled for 200 ms selected CPU but took
562.864 ms, so it missed that illustrative target.

The respective median request times were 1,311.542 and 563.963 ms, but the
sample is tiny, sequential and non-randomized; the first GPU call was cold.
The GPU tunnel precludes a co-located latency conclusion. Router token-price
arithmetic was $0.001652 and $0.000630 respectively, with local and GPU
infrastructure rates configured to zero. Those figures are not total cost,
provider invoices or proof of savings. One MI300X was quoted at $2.59/hour;
recorded creation to verified teardown was 481.361 s: **estimate (hours ×
list rate), invoice pending: approximately $0.346313**. A previous bootstrap
failure has the same method's estimate of approximately $0.096101. These
are not actual charges; invoice reconciliation is not a v4.5 blocker.

Provenance:

- Router source revision `2486d31208d1fd3b13bd0021d801004a8445f343`; its relevant
  source matches merged #518. Router binary SHA-256
  `e48e56a4963ced08b59660e0fb03abcc0b581006cb2e1886b7d7c2f7867bb8fd`.
- Dataset SHA-256 `7445c70652a77148133f3e43acc5da6fa90a6fed54dde2a18f6b385a13ad5eb4`.
- Local Qwen2.5-0.5B-Instruct revision `7ae557604adf67be50417f59c2c2f167def9a775`;
  GPU Qwen2.5-7B-Instruct revision `a09a35458c702b33eeacc393d103063234e8bc28`.
- Official `rocm/vllm` image digest
  `sha256:04b7c83e7fec4ea0e6d38fd77025aa39b5f7d0a61ecd49a977f1a520616fdbab`;
  vLLM `0.23.1.dev1+g9ddef7117.d20260901`, PyTorch `2.11.0+rocm7.14.1`.
- The independent watcher verified exact droplet and provider key absent and
  local key removal; CPU and tunnel processes stopped. Hardware/runtime logs,
  intent/hash replay, teardown and raw bounded evidence are in the shared
  artifact library under `router / v45-proof / 427`, snapshot `a371eee3`.

This run does not establish production quality, privacy, latency SLA,
vendor-neutral results or costs. A separate bounded NVIDIA observation is
published; representative tasks, load variation and the remaining #427
acceptance remain open.
