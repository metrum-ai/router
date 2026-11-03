<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Failures and things that did not run

Nothing here is a pass. Each line is what happened on the box.

| # | Check | Result | What happened |
|---|---|---|---|
| 1 | Concurrency sweep (1/8/32 workers) | **not completed** | The load driver aborted in the 32-worker cells. Request log shows 24 x `429 concurrency-exceeded` and 3 x `429 tpm-exceeded`: the generated harness caller carried default rate ceilings. Raising them to `concurrent=64`, `tpm=2 000 000`, `rpm=600` and restarting fixed the cause, but the subsequent restarts failed on a caller allow-list error and the rental window closed before a clean sweep. No number is claimed. |
| 2 | Escalation group | **not run on this box** | Policy: no provider keys on rented third-party hardware. Escalation is proven by the v47-hier #434 run. |
| 3 | Caller group grants (`RESTRICTED_TOKEN`) | **not proven** | The restricted caller was minted into a copy of the configuration that the router never loaded; the check answered `401 unauthorized` instead of `404 model_not_found`. Needs a re-run with the caller written before the router starts. |
| 4 | Admin UI | pass | Basic auth answered `200` with credentials and `401` without. |

## Hardware-stage findings already carried back into the tree

1. The runtime image runs as uid 65532; a fresh tier directory owned by the
   operator makes the caller-token write and the usage database fail with
   `permission denied`. Each tier README now says `chown -R 65532:65532`.
2. `--quantization fp8` is applied at **load** time, so disk usage follows the BF16
   checkpoint (64 GB for Qwen3-32B), not the 33 GB resident size. Disk guidance and
   a disk-versus-memory note are in the tier README and `SIZING.md`.
3. `gpu_memory_utilization=0.06` for the 1.7B slot fails with `No available memory
   for the cache blocks`; the tiny slot now plans 0.12 (datacenter) / 0.09 (edge)
   with a 3 GiB overhead allowance.
4. The load driver requires every response to report exactly `--output-tokens`; a
   plain filler prompt stops at EOS after ~130 tokens. The sweep prompt now ends
   with an instruction to keep generating.
5. A caller whose `allow` list names a group the tier does not define makes the
   router refuse to start (`caller ... allows unknown model group ...`).
