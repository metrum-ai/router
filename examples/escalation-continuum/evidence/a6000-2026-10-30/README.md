<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Synthetic CPU / NVIDIA A6000 / frontier measurement

On 2026-10-30 the mixed benchmark ran each of 12 invented arithmetic cases
once on frontier-only and once on escalation, using real local CPU, NVIDIA
RTX A6000 and hosted frontier inference through the router—not mocks. GPU
traffic traversed an SSH tunnel. See [result.json](result.json) for the 24
raw records and [summary.json](summary.json) for derived values.

Both paths got 12/12 exact responses. Escalation chose CPU for six, A6000 for
three and frontier for three; the frontier-only baseline made 12 hosted
attempts, escalation three. The private-labelled case is invented arithmetic:
the baseline sent it to the frontier, while escalation selected CPU. This
case-ID policy is not a privacy or complexity classifier. The case labelled
for 200 ms selected CPU but took 531.875 ms, missing the illustrative target.

The respective median request times were 1,254.026 and 580.305 ms. The sample
is tiny, sequential, non-randomized and includes tunnel timing. AMD and NVIDIA
runs used different hosts, network paths and vLLM builds, and one AMD GPU call
was cold; they do not support a controlled vendor performance comparison.
Router token-price arithmetic was $0.001482 and $0.000510, respectively,
with CPU and GPU infrastructure rates configured to zero: neither figure is a
provider invoice, total task cost or proof of savings. The NVIDIA provider's
post-deletion `cost_estimate` was $0.0387500787; it is not reconciled billing.

Provenance:

- Router source revision `2486d31208d1fd3b13bd0021d801004a8445f343`; its relevant
  source matches merged #518. Router binary SHA-256
  `e48e56a4963ced08b59660e0fb03abcc0b581006cb2e1886b7d7c2f7867bb8fd`.
- Dataset SHA-256 `7445c70652a77148133f3e43acc5da6fa90a6fed54dde2a18f6b385a13ad5eb4`.
- Local Qwen2.5-0.5B-Instruct revision `7ae557604adf67be50417f59c2c2f167def9a775`;
  GPU Qwen2.5-7B-Instruct revision `a09a35458c702b33eeacc393d103063234e8bc28`.
- Official `vllm/vllm-openai` image digest
  `sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34`;
  vLLM `0.32.0`, PyTorch `2.13.0+cu130`; driver `570.195.03`.
- Independent watcher verified the exact instance and registered key absent
  and local key removal; CPU and tunnel processes stopped. Hardware/runtime
  logs, replay, teardown and bounded evidence are in the shared artifact
  library under `router / v45-proof / 427`, snapshot `c2d3e27f`.

This run does not establish production quality, privacy, SLA, controlled
vendor comparison or actual cost. Representative tasks, stronger load and
latency coverage, provider billing reconciliation and #427 acceptance remain
open.
