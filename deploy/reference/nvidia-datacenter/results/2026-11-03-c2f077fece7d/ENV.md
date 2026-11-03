<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run environment: nvidia-datacenter

| Item | Value |
|---|---|
| Tier | nvidia-datacenter |
| Commit | `c2f077fece7dd07d6792403ab35deb055accc263` (main, includes this tier and its harness) |
| Recorded at (UTC) | 2026-11-03T06:53Z |
| Router base URL | `http://127.0.0.1:8080` (co-located with the model servers) |
| GPU | 1× NVIDIA H100 **PCIe** 80 GB, 81 559 MiB, driver 570.195.03 |
| Host | 28 vCPU, 177 GB RAM, 100 GB root + 750 GB ephemeral disk, kernel 6.8.0-90-generic |
| Container runtime | Docker 29.1.3 with buildx 0.30.1 |
| Model-server image | `vllm/vllm-openai:v0.33.0@sha256:d825baf9ce4b1b6bfc5ecc7b79880c7e6748c65a72d697ce3d47607aafa84c6e` |
| Models | `Qwen/Qwen3-1.7B@70d244cc…` (`local-tiny`), `Qwen/Qwen3-32B@9216db57…` (`local-chat`, FP8 at load) |
| `gpu_memory_utilization` | `local-tiny` 0.12, `local-chat` 0.84 |
| Deployment | the tier's `compose.yaml` unmodified |

## Run-level changes (stated, not hidden)

- Docker's data root was moved to the 750 GB ephemeral disk: `--quantization fp8`
  downloads the BF16 checkpoint (64 GB), which does not fit the 100 GB root beside a
  35 GB image.
- The **harness caller** was minted for the run and its rate ceilings and quotas were
  cleared (`rpm/tpm/concurrent = 0`, quota `requests/tokens = 0`), because a sweep
  issues thousands of requests in minutes. Every real caller in the tier config keeps
  its quota.
- The router image was built on the box from the commit's Dockerfile with the release
  build args, **with the docs-site build step removed**: main's docs tree does not
  compile at this commit (MDX error in `docs-site/docs/evaluation/tool-call-matrix.md`,
  reported separately). The router binary, the admin UI and the tier configuration are
  unaffected; `/docs/` is empty in that image.

## What this run does not cover

- **Escalation is NOT RUN on this box.** No provider key is placed on rented
  hardware. Escalation is proven by the v47-hier #434 run.
- **Caller group grants were not exercised by the suite**: minting a second caller
  with `callers generate --write` drops the previously minted caller's entry (#755),
  so the suite's own token answered `401` after the restricted caller was minted. The
  behaviour was verified by hand on the same box — a caller without the group gets
  `404 model_not_found` for `escalate` — but it is reported as a manual observation,
  not as a suite pass.
