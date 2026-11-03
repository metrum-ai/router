<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run environment: nvidia-datacenter

| Item | Value |
|---|---|
| Tier | nvidia-datacenter |
| Commit | `e956434e36ec2e4ac18def53586f5489a539f022` (tier config identical to `472d08b8`; the only later change is the sweep wrapper) |
| Recorded at (UTC) | 2026-11-03T02:05Z |
| Router base URL | `http://127.0.0.1:8080` (co-located with the model servers) |
| GPU | 1x NVIDIA H100 PCIe, 81 559 MiB, driver 570.195.03 |
| Host kernel | 6.8.0-90-generic, 28 vCPU, 177 GB RAM, 100 GB root + 750 GB ephemeral disk |
| Container runtime | Docker 29.1.3 with buildx 0.30.1 |
| Model-server image | `vllm/vllm-openai:v0.33.0@sha256:d825baf9ce4b1b6bfc5ecc7b79880c7e6748c65a72d697ce3d47607aafa84c6e` |
| Models | `Qwen/Qwen3-1.7B@70d244cc86ccca08cf5af4e1e306ecf908b1ad5e` (`local-tiny`), `Qwen/Qwen3-32B@9216db5781bf21249d130ec9da846c4624c16137` (`local-chat`, FP8 at load) |
| gpu_memory_utilization | `local-tiny` 0.12, `local-chat` 0.84 |
| Router image | built on the box from the commit's Dockerfile with the release build args (`docker build`, tag `metrum-ai-router:4.7.0-refcfg-linux-amd64`, image id `db4735c01e3c`, 340 MB) |
| Deployment | the tier's `compose.yaml` unmodified |

## Run-level changes (stated, not hidden)

- The router image was built with `docker build` and the release build args rather
  than `make docker-image-no-docs`, because that target's Go smoke cannot run on a
  host without a Go toolchain. The Dockerfile and build args are the shipped ones.
- The **harness caller** was granted `rate.concurrent=64`, `rate.tpm=2 000 000`,
  `rate.rpm=600` for the sweep. Without that, a 32-worker cell is refused by the
  caller's default limits (24 x `429 concurrency-exceeded`, 3 x `429 tpm-exceeded`
  in the request log). The committed caller in `config.yaml` is unchanged.
- Docker's data root was moved to the 750 GB ephemeral disk: `--quantization fp8`
  downloads the BF16 checkpoint (64 GB for Qwen3-32B), which does not fit a 100 GB
  root beside a 35 GB image.
- The admin password hash is a value generated on the box; the committed
  configuration's admin user is unchanged.

## What this run does not cover

- **Escalation is NOT RUN on this box.** No provider key is placed on rented
  hardware. Escalation is proven by the v47-hier #434 run.
- **No concurrency claim.** The sweep never completed (see `FAILURES.md`), so this
  directory carries no p95 and no sustained-concurrency number.
- **Caller group grants were not exercised** in the run of record: the restricted
  caller minted for the check did not reach the configuration before an unrelated
  router restart failed, and the check returned `401` rather than `404`.
