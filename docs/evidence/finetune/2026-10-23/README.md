<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Qwen3.8-27B fine-tuning — live work in progress

Track FT, issue #326. Checked 2026-10-23 UTC. **Training is not complete.
No promotion claim is supported by this evidence.**

## Progress after maintainer decisions

**05:01 UTC pre-outcome amendment:** dataset QA found the initial JSON-only
converter rejected Glaive's mixed Python-literal quoting. Its random held-out
split had **zero tool targets** (training had only 2/2000), so the first adapter
is superseded and not promotable. No held-out FT outcome inference was run.
Fixed with safe literal parsing, regression tests and seeded 2:1 tool/text
stratification; corrected counts are train1333/2000, val333/500, test200/300.
Current `dataset-manifest.json` and the amended protocol identify the corrected
hashes; the original signed commit below preserves the flawed preflight.
The first full training and vLLM native/tool/schema smoke completed, but are
**not corrected-data model evidence**. Fireworks first upload validated with
zero warnings; its 2×B300 default deployment was still CREATING when stopped
immediately on discovery. Deployment deletion was API-verified04:56:23Z and
uploaded model deletion GET404 verified05:00:52Z. Official vLLM was stopped to
release VRAM; corrected hardware smoke + full retraining are now running.

Preregistration and original scorer were published **before any FT outcome
inference** in verified GitHub commit
[`a5a60109d20e3eaa5c4275efc4905cd4a7d2c920`](https://github.com/metrum-ai/router/commit/a5a60109d20e3eaa5c4275efc4905cd4a7d2c920).
The held-out test hash is in `dataset-manifest.json`; the scalar gate contract
and full threshold companion are under the example's `eval/`. Hardware smoke
and public human-judge calibration did not consume held-out FT test cases.

The human-label gate below was resolved: use a licensed PUBLIC human-labeled
corpus, not in-house labeling. `judge-calibration.json` records 50 distinct
MT-Bench human judgments from revision
`f7d2896d2cc5d80f8b55c2bbc722613555233c25`, CC-BY-4.0 per pinned README.
Attribution: Zheng et al., *Judging LLM-as-a-Judge with MT-Bench and Chatbot
Arena* (2023), arXiv:2306.05685. The `human` split, not `gpt4_pair`, was used.
Judge `gpt-4o-2024-11-20`, temperature 0: agreement 0.60, bootstrap 95% CI
[0.46, 0.74], 10,000 resamples seeded 326. One vote per distinct question reduces
repeated-question correlation. This weak pairwise agreement does not validate
structured tool calling. All 50 calls went through localhost worktree router
`frontier-ref`; only aggregate scalars and question IDs were saved.
57,166 input / 98 output tokens imply $0.143895 at $2.50/$10 per million, plus
$0.000045 direct availability smoke. These are estimates, not billing records.
The initial dated GPT-4.1 snapshot was unavailable; no judgments were collected
with it.

`go build -o /workspace/router-v430/ft-step0/router ./cmd/metrum-ai-router` and
`/workspace/router-v430/ft-step0/venv/bin/python
examples/fine-tune-qwen3p8-27b/eval/router_e2e.py --router-bin
/workspace/router-v430/ft-step0/router` passed: synthetic two-target FT group,
file-creation tool call, tool-result continuation, 44 requests reached both
weighted targets (17/27), request quota enforced, unique prompt marker absent
from usage DB and logs. These are synthetic plumbing results, not trained-model
evidence. Promptfoo 0.124.1 mock suite passed 2/2, including rejection of a planted
hallucinated-tool mismatch using an inverse assertion.

Vast attempts: instance 57316233 was deleted after a nullable-state polling bug
(0.00156012 h × $6.380208/h = estimated $0.009954). Instance 57316282 reached
running but SSH rejected the fresh API-attached key; it was deleted
(0.01496866 h × $7.630208/h = estimated $0.114214). Both API absences verified;
keys deleted and watchdogs stopped. A provider-generated instance credential
unexpectedly appeared in the first create response; the instance was destroyed,
local log redacted, and response logging now redacts credential fields. No
credential values are included here.

New maintainer direction: Shadeform primary, Axolotl primary/Unsloth fallback,
approved H100/H200/B200/RTX PRO 6000 Blackwell up to 8 GPUs. Separate on-prem GPU
cap $1,000, stop $800; keep Fireworks modest. The committed Axolotl YAML follows
its explicit Qwen3.8 recipe inspected at upstream commit
`777990d36ab92dcdc8b62d5ad07dc30b9618ff25`; FSDP2 overlay and two-step smoke
renderer are included. **Real hardware smoke passed**: see
`hardware-smoke-runtime.json` and `axolotl-package-versions.txt`. Axolotl
0.21.1.dev0, BF16 rank16/alpha32, two 512-token steps, 47.31s trainer runtime,
88.746s wrapper wall time, 53.11GiB peak allocated. All 800 adapter tensors were
verified to belong to `language_model.layers`, not vision or LM head. The image
supports Qwen3.8, so Unsloth fallback was unnecessary. Reference causal-conv1d
was used (slower than its optional optimized kernel); do not treat tiny-smoke
throughput as a production result. Full 2,000-row, two-epoch training is running.
Current Shadeform host: massedcompute RTXPro6000 1×96GB, 750GB disk,
$2.19/h; instance `3b0c7858-2854-4dda-b121-b0fdea186fbe` is provisioning with
fresh key and independent three-hour teardown watchdog. This is the cheapest
advertised approved candidate with ≥500GB disk; cheaper 250GB SKU does not meet
the disk requirement. Actual SSH hardware check: RTX PRO 6000 Blackwell Server
Edition, 97,887MiB VRAM, driver 580.126.09, Docker 29.1.5, ~713GB filesystem.
The pinned 54GB-class base snapshot downloaded remotely in ~240s (end-to-end,
including metadata/file overhead, not an isolated network benchmark). Host network
speed is not yet independently benchmarked. Production sequence-cap fit and
training completion must not be inferred from the 512-token smoke.
No Fireworks jobs or deployments have been created.

## Historical Step 0: model and Fireworks decision

Authenticated Python `urllib.request` GETs (credentials read from the environment,
never included in argv or saved request headers) returned:

| Endpoint | HTTP | Observation |
| --- | --- | --- |
| `https://huggingface.co/api/models/Qwen/Qwen3.8-27B` | 200 | Public, ungated; revision `1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0`; Apache-2.0 |
| Hub `LICENSE` and `config.json` at that revision | 200 | Apache License 2.0 text; `Qwen3_5ForConditionalGeneration`, BF16, 64 text layers |
| `https://api.fireworks.ai/v1/accounts/fireworks/models/qwen3p8-27b` | 200 | READY; `baseModelDetails.tunable=false`, `supportsLora=true`, `supervisedLoraTunable=true`, `supportsServerless=false` |

Use the issue's explicit fallback: **train standalone, upload the adapter to
Fireworks, and serve from a dedicated deployment**. Do not claim a managed SFT
run: the required general `tunable=true` check did not pass. The REST equivalent
was used because `command -v firectl` found no installed CLI. The extra
`supervisedLoraTunable` flag does not override the issue's prescribed gate.

Metadata lists 18 safetensors shards. We fetched metadata/config/license, not
54 GB of weights. `MODEL_LICENSES.md` already inventories this model and exact
revision; preserve the owner's license/NOTICE obligations when downloading.

## Dataset selection

Select `glaiveai/glaive-function-calling-v2`, revision
`e7f4b6456019f5d8bcb991ef0dd67d8ff23221ac`. The authenticated Hub metadata GET
returned HTTP 200, `gated=false`, and `license:apache-2.0`; the pinned README GET
also returned HTTP 200 and declares Apache-2.0. This permits the proposed
commercial text-only training subject to Apache-2.0 conditions. Retain upstream
attribution and any notices; do not commit the real dataset.

Alternative `Salesforce/xlam-function-calling-60k` declares CC-BY-4.0 but has
an automatic access gate requiring personal details and license/citation
acknowledgment. No terms were accepted or personal details submitted. It was
not selected.

## GPU offers (quotes, not charges)

`GET https://api.shadeform.ai/v1/instances/types?available=true` returned HTTP 200
(no 401). A B200 offer from Verda in Helsinki lists 192 GB VRAM, 1,500 GB disk,
CUDA 12.8 OS, and 652 cents/hour ($6.52/hour). Network speed is not present in
that response, so this alone is not a verified qualifying offer. No available
8× B200 type was returned.

Vast.ai `GET https://console.vast.ai/api/v0/bundles/` with JSON query `q` returned
HTTP 200. Filters: B200, rentable, not rented, 1 or 8 GPUs, disk ≥500 GB,
up/down ≥1,000 Mbps, CUDA ≥12.8, on-demand, `allocated_storage=500`, ascending
`dph_total`. At 02:47:30 UTC:

| Offer | GPUs | GPU $/h | Disk $/h | Total $/h | CUDA max | Down/up Mbps | Verification |
| --- | ---: | ---: | ---: | ---: | ---: | --- | --- |
| 51577280 | 1 | 6.25 | 0.130208 | 6.380208 | 13.0 | 21495.2 / 3394.9 | verified |
| 51575925 | 1 | 7.50 | 0.130208 | 7.630208 | 13.0 | 55458.9 / 4983.0 | verified |
| 57220320 | 8 | 75.00 | 0.130208 | 75.130208 | 13.2 | 9620.8 / 7998.3 | deverified |

Proposed primary: offer 51577280 if still available on resumption; it is cheaper
than Shadeform and meets the reported disk/network/driver requirements. These
are marketplace claims, not measured host performance. Requery before renting;
network transfer charges are additional. No 8-GPU rental is authorized by this
plan: full SFT still requires the maintainer's go-ahead.

An initial PUT to `/api/v0/bundles/` returned 404; the GET query above succeeded.
The initial GET omitted allocated storage and understated the hourly disk cost;
only the corrected 500 GB quote in this table should be used for budgeting.

## Original plan and human-input gate (resolved)

**Historical preflight note; the maintainer subsequently authorized public human
labels, as recorded above.** Before spending, obtain **50 genuinely human-labeled non-tool calibration
items**, or a maintainer decision explicitly changing that requirement. No such
set was supplied or found in the worktree/workspace. AI-produced labels cannot
be represented as human labels. Needed: the designated reviewers/source and a
separate, licensed calibration cohort with the agreed answer-quality rubric.
Keep this cohort separate from the held-out test set and do not publish prompts
or responses in evidence.

After that input:

1. Implement and offline-test dataset preparation, canonical schema, synthetic
   fixtures, provisioning/teardown for both marketplaces, training, LoRA upload,
   official vLLM serving, router groups, promptfoo scorers, and outcome-gate cases.
2. Freeze vision tower and LM head; LoRA rank 16/alpha 32, BF16, checkpointing;
   pin actual container digests and package versions before provisioning.
3. Prepare ≤20k train / 500 val / 300 held-out test, dedup and reject normalized
   leakage; hash the held-out split and preregister thresholds before evaluation.
4. Train once on 1× B200 for the mandated fallback; upload those weights to
   Fireworks. Clearly distinguish serving-stack parity from independently
   trained arms. Tunnel the B200 service to localhost; never expose model ports.
5. Run router-backed quality/regression/load checks and tool-result continuation
   against a worktree-built localhost router. Pin the judge and report actual
   human agreement. Emit aggregates only and run `fixed_model_outcome_gate.py`.
6. Verify every resource deleted via its API. Update costs after every paid step;
   stop at $800 total against the $1,000 cap. Publish a signed, validated PR.

## Historical Step 0 spend and validation (before live work above)

| Resource | Created | Hours | Cost |
| --- | ---: | ---: | ---: |
| GPU instances | 0 | 0 | $0 |
| Fireworks training jobs/deployments | 0 | 0 | $0 |
| Total | | | **$0** |

No paid step, model invocation, dataset download, training, eval, or resource
creation was performed. No instance IDs or SSH keys were created; no teardown
is needed for this track's work. This does not assert that the account has no
resources belonging to other tracks.

Commands actually run: `gh issue view 326 --repo metrum-ai/router --json
body,comments --jq '.body, (.comments[]|.body)'` (success); `command -v firectl`
(not installed); authenticated Python metadata GETs and Vast offer query as
specified above (statuses recorded). The NVIDIA recipe documentation URL
returned HTTP 403; recipe validation remains outstanding. A GitHub release
lookup for `fw-ai/firectl` reported no release.

`make docs-qa` passed: schema check, 54 public-face tests, 6 canonical-product
tests, 9 origin tests, public/origin/version/sidebar checks, and config-example
validation. Full output is retained outside Git in
`/workspace/router-v430/ft-step0/docs-qa.log`.

At Step 0 no implementation, training, offline feature tests, local router e2e,
or outcome gate had run. The progress section above supersedes those historical
observations. Full live training, serving and outcome evidence are still pending.
