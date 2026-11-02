<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Reference deployment configurations

One directory per `<vendor>-<tier>`, each deployable as it stands on the hardware
it names. The purpose is that a platform team can take the hardware it already
owns, deploy the matching directory, and move to another vendor by changing
configuration and image pins, not by redesigning the deployment.

Every directory has **the same file set**:

| File | Contents |
|---|---|
| `README.md` | Hardware and bill of materials, pinned images, model groups, deploy/teardown commands, measured results, actual cost, limits. |
| `SIZING.md` | Sizing method, per-model memory arithmetic, target latency, the concurrency the box is expected to sustain, and what was measured. |
| `config.yaml` | Router configuration: providers, model groups, escalation group, caller quota, admin UI and reports. |
| `compose.yaml` | Deployment for the model servers and the router on that hardware. |
| `images.lock` | Every image reference with its pinned digest, and the model revisions served on that tier. |
| `results/` | Measured evidence committed from a run on a named commit, plus what was not measured. |

`scripts/check_reference_configs.py` enforces the file set, the image policy, the
README sections, the digest pins and a valid router configuration. It runs in CI
(`make test-reference-configs`).

## Tiers

| Directory | Hardware class | Serving images | Router model groups | Validation state |
|---|---|---|---|---|
| [`nvidia-datacenter/`](nvidia-datacenter/) | 1× NVIDIA 80 GB datacenter GPU (H100 80 GB class) | `vllm/vllm-openai` | `local-tiny`, `local-chat`, `escalate` | structure and pins committed; hardware run pending |
| [`nvidia-gb10-edge/`](nvidia-gb10-edge/) | 1× NVIDIA GB10 system, 128 GB unified memory (aarch64) | `vllm/vllm-openai` (arm64) | `local-tiny`, `local-chat`, `escalate` | structure and pins committed; hardware run pending |
| [`amd-mi350p-datacenter/`](amd-mi350p-datacenter/) | 1× AMD Instinct MI350P 144 GB (gfx950) | `rocm/vllm` (`gfx950`) | `local-tiny`, `local-chat`, `local-coder`, `escalate` | structure and pins committed; hardware run pending |
| [`amd-strix-halo-edge/`](amd-strix-halo-edge/) | 1× AMD Ryzen AI MAX+ 395, 128 GB unified memory (gfx1151) | `rocm/vllm` (`gfx1151`) | `local-tiny`, `local-chat`, `escalate` | structure and pins committed; hardware run pending |

A tier README says `hardware run pending` until `results/` holds a measured run
from a named commit. Pending is not a pass, and no tier claims a measured
concurrency before one is committed.

## Image policy

- Model servers use **official upstream images only**: `vllm/vllm-openai` for
  NVIDIA and `rocm/vllm` for AMD. No llama.cpp, Ollama, LM Studio or community
  images, and no vendor wrapper images, in a reference configuration.
- Every model-server image is pinned **by digest** (`repo:tag@sha256:...`) in both
  `compose.yaml` and `images.lock`. The checker fails on a tag without a digest,
  a digest that disagrees with the lock file, or an image outside the two
  upstream repositories. Model weights are pinned by Hugging Face commit
  revision in the serving arguments.
- The router image is **operator-supplied** and never `latest`:
  `ROUTER_IMAGE` is required by `compose.yaml` and the router image digest is a
  property of the release an operator deploys. `images.lock` records the
  reference form (`metrum-ai-router:<version>-linux-<arch>`) rather than a
  registry this repository does not own.

## Model groups and the escalation path

All tiers expose the same group shape, so client configuration does not change
between vendors:

- `local-tiny`: the small always-resident model, used for smoke tests and cheap
  classification work.
- `local-chat`: the primary local chat model.
- `local-coder`: a second local slot where the tier has memory for it (the
datacenter AMD tier; the 80 GB NVIDIA and 128 GB unified-memory edge tiers do
not, and say so in their Limits).
- `escalate`: the escalation path off the box. It points at an
  OpenAI-compatible frontier endpoint (default `https://api.openai.com/v1`) with
  a key the operator supplies. Callers must be granted the group explicitly; a
  caller without it gets `404 model_not_found` (the group does not exist for that
caller), not a silent local answer.

Cost control is caller `quota` (requests and tokens per day and month) plus, for
`dynamic_score` and `semantic` groups, `spend_ceiling`. There is no universal
hard-dollar ledger, and a static group does not consult `spend_ceiling`;
`SIZING.md` and the tier README state the limits that do apply.

## Deploy a tier

```sh
cd deploy/reference/<vendor>-<tier>
mkdir -p state logs config
cp config.yaml config/config.yaml
export ROUTER_IMAGE=metrum-ai-router:<version>-linux-<arch>   # required, never latest
export LOCAL_VLLM_API_KEY="$(openssl rand -hex 24)"
export REFERENCE_ADMIN_PASSWORD_HASH="$(htpasswd -bnBC 12 '' 'change-me' | tr -d ':\n')"
export OPENAI_API_KEY=           # optional; leave empty to keep `escalate` unused
docker compose up -d
curl -fsS http://127.0.0.1:8080/readyz
```

Then generate the caller token the demo script uses (the checked-in
`token_sha256` is an all-zero placeholder that cannot authenticate anyone):

```sh
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-routerctl \
  router callers generate --config /app/config/config.yaml --id reference-demo
```

## Validate a tier

```sh
deploy/reference/harness/functional_smoke.py <vendor>-<tier>   # /readyz, /v1/models, one completion per group, auth negatives
deploy/reference/harness/concurrency_sweep.py <vendor>-<tier>  # sizing method from benchmarks/overhead/load.py
```

The functional suite also proves group grants when `RESTRICTED_TOKEN` is set to a
caller token that was not granted a group: such a request must not reach the
upstream (the router answers `404 model_not_found`).

Both accept `--out`/`--out-dir` so a run writes into the tier's `results/`;
the model servers also publish on loopback, which is how the sweep measures the
direct lane. Nothing in a tier listens on a non-loopback address except the
router port.

One command collects a whole run, including `ENV.md` with the host, driver,
image digests and model revisions:

```sh
RUN_COMMIT=$(git rev-parse HEAD) ROUTER_TOKEN=... \
  python3 deploy/reference/harness/run_tier.py <vendor>-<tier>
```

It writes `results/<date>-<commit>/` with `ENV.md`, `functional.json`,
`sweep.json` and the sweep's raw driver output. A failed suite or sweep keeps the
partial evidence and exits non-zero: the directory is then a record, not a pass. A run is only evidence
when it names the commit it ran on and the hardware it ran on; the tier README
then replaces `pending` with the measured numbers, the actual cost, and the
limits.

## Move between vendors

Only three things change when a deployment moves to another tier:

1. `compose.yaml` of the destination tier (same service names, same ports, same
   router image variable).
2. `images.lock` pins of the destination tier.
3. `config.yaml` only where the tier has a different set of groups (`local-coder`
   exists on the datacenter tiers and not on the 128 GB unified-memory edge
   tiers).

Caller tokens, group names, admin URLs, quotas and the escalation provider stay
identical, so client configuration and dashboards do not move.

## Add a tier

Copy the directory whose form factor matches the new hardware, replace the
hardware table, `images.lock` pins, `SIZING.md` arithmetic and `compose.yaml`,
and leave the rest of the layout alone. If the tier uses an upstream image that
is not yet pinned, resolve the multi-arch digest first:

```sh
docker buildx imagetools inspect vllm/vllm-openai:v0.33.0
```
