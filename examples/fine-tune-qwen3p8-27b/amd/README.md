<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# AMD Instinct preparation (#359)

Metrum AI's MI325X path has passed actual official-ROCm base/template/native and
PEFT two-step/fixed-full-training checks. Pinned Axolotl dependency resolution
failed, and unmerged ROCm vLLM LoRA loading failed; the documented alternatives
are PEFT training and safe BF16-merged serving. Merged smoke/load checks pass;
frozen AMD/base qualification completed but original exact/overall quality gates
**FAIL: do not promote** (7/200vs9/200 JSON-text exact). Native180/200both is
separate and cannot rescue the original gate. Orchestrator API-verified teardown
completed19:23Z (404/tagempty/firewall+cloudkeyabsent). Healthy shared-teacher
replacement and NVIDIA matched-merge comparison remain separately owned;
no quality-equivalent ROI or pure hardware advantage claimed.
See `docs/evidence/finetune-amd/2026-10-23/` from the repository root.
Only #358's governed router export may supply training/validation/held-out rows;
never run the direct-download preparation instructions for this track.

## Teacher decision and shared freeze coordination

The maintainer [authorized the Fireworks-hosted open-weight teacher](https://github.com/metrum-ai/router/issues/358#issuecomment-6366186842)
for this synthetic Acme/mock-tool technical test. This gate is cleared; it is
**not general customer-output permission**. Record `teacher_terms_decision` in
the actual export manifest and evidence, including the decision URL and pinned
terms SHA256 `78c0dfb857eb860d15cfc2dc7296f3b19950d3145b0e5a40bc46e28cfbd19d30`.
Users must choose a teacher whose model license **and provider terms permit
training on outputs**, and verify that before enabling export for training.

`freeze-proposal.json` records the NVIDIA-compatible recipe (seed355), AMD-only
TF32 difference, proposed cohort/mix and replay wiring for FT/traces agreement.
It is **not a final preregistration or training authorization**. The seven-row
mixed real-teacher engineering export passes the FT consumer, but is too small
for training. All engineering probes must be excluded from the fresh cohort.
Use the shared run2 `axolotl.yaml` and validated opaque-tool training views when
rendering the final recipe, not the historical standalone run1 recipe below.
The actual dataset/view/backend/gate hashes and exact generation limits must be
signed and frozen by both tracks before either rents/trains/evaluates.

## Final cohort and execution freeze

The final 1186/221/300 export is validated. The frozen data/recipe/evaluation
protocol is `docs/evidence/finetune-amd/2026-10-23/shared-freeze.json` from the
repository root. It supersedes proposed sizes and TF32 differences above:
TF32=false on BOTH accelerators. Exactly identical export and Axolotl-view bytes
are required; hashes and the explicit two-policy-masked-native-case versus
private-source-gold oracle mapping are frozen there. No teacher answer replay,
original endpoint substitution, regression relaxation or score-based refills.
Hardware-only image/driver/package evidence is recorded on bootstrap; two-step
smoke/cache/template/native checks still gate actual full training. An AMD
hardware failure must be reported, never presented as parity or quality success.

## Before requesting paid hardware

1. Obtain #358's manifest, train/val/test envelopes and allowlisted deterministic
   replay backend outside Git. Coordinate the exact schema with the traces/FT
   tracks, validate hashes/counts, time boundaries, trace/task disjointness,
   capture/privacy/admin audit attestations and teacher-output rights. A hash
   alone proves neither consent nor correct capture. Reject missing contracts.
2. Freeze the shared NVIDIA/AMD export hash, recipe, replay/checker, general
   regressions, denominators and unchanged promotion limits **before** training
   or looking at held-out outcomes. Proposed recipe: BF16 SDPA, rank8/alpha16,
   LR1e-5, one epoch, batch16, 8192 cap, text decoder only. This is provisional
   until the run-2 owner confirms parity. Preserve tools and native supervision.
3. Select `rocm/pytorch` and `rocm/vllm` versions matching the droplet host driver
   and MI325X/gfx942, and supporting the pinned Qwen3.8 architecture. Resolve
   registry tags to digests. Never use `latest` or infer architecture support
   from MI325X support alone. `plan.py` requires official immutable references.
4. Request an orchestrator-created MI325X only after the dataset is ready (or
   explicitly a bounded <1h environment smoke). No DO API keys here. Use a fresh
   `~/.ssh/amd359` key, report only its public key. Budget $300, stop at $240;
   at the issue's approximate $3.80/h that is ~63.16h **including idle/setup**.
   Verify actual rate and disk charges; independent orchestrator deadline needed.

## Remote security, environment and toolkit smoke

The orchestrator must install an SSH-only source-restricted cloud firewall.
Run `bash amd/first-boot.sh` as root before loading models; it disables the
marketplace startup hook, stops the `rocm` container, disables active Caddy and
fails on any remaining non-loopback non-SSH TCP/UDP listeners. Do not silently
stop unrelated services to bypass that failure. Run `bash amd/capture-env.sh
/private/env` and review/redact hardware IDs before publishing. Repeat the listener
check after serving. Never dump environment variables or full Docker inspection.

Axolotl is the first toolkit to try, **inside official rocm/pytorch**, not its
NVIDIA image. The upstream AMD HPC guide at commit
`777990d36ab92dcdc8b62d5ad07dc30b9618ff25` targets ROCm5.7/gfx90a and Llama;
it is not evidence of MI325X/Qwen3.8 compatibility. Install the pinned Axolotl
source and record the resolved package inventory/image digest without replacing
ROCm torch with CUDA torch. Keep that installation recipe and derived environment
inventory in evidence. No install/version combination is approved yet.

On actual hardware verify `torch.version.hip`, BF16 and model loading, then run
**two optimizer steps** on a tiny subset of exported training rows. Render using:

```sh
python3 amd/render.py --recipe standalone/axolotl.yaml --data-id "$MANIFEST_SHA256" --smoke --out /private/smoke.yaml
python3 amd/plan.py smoke --image "$PYTORCH_DIGEST"
```

The planner only prints commands; inspect them before executing on the owned
host. Mount the private base/data/configs at `/workspace/amd359` (container
`/data`). Training runs network-isolated with no credentials. Transfer/download
the immutable base beforehand without persisting HF credentials. The canonical
training view must come from the validated export; do not discard provenance in
the archive. Smoke uses `/data/canonical/smoke.jsonl`, never held-out data.

Inspect prepared-cache token/template parity and supervised tool markers against
NVIDIA. Record trainer and wrapper wall time, actual trained-token count, steps,
loss, peak allocated/reserved memory and `amd-smi` sampling. Do not treat a
process exit alone as proof of two completed optimizer steps. Only then render
without `--smoke` and consider training. Cache identities include recipe and
manifest hash. No automatic train approval marker is provided.

If Axolotl fails, preserve the error/package inventory and reason. Use a documented
ROCm-supported Transformers/TRL/PEFT alternative in the official PyTorch image,
freeze the dependency/recipe amendment, and repeat the two-step smoke. There is
no claim this fallback has run or that Unsloth supports this exact model/hardware.

## Serve, tunnel and router evaluation wiring

`python3 amd/plan.py serve --image "$VLLM_DIGEST"` prints a LoRA serving command.
It publishes only **host 127.0.0.1:18590**. Container `--host 0.0.0.0` is confined
to the container network, not a public host bind. Confirm the selected vLLM
supports this model's hybrid-attention LoRA; if not, record the error and test a
BF16 merged adapter with a separately documented command. No quantization change.

```sh
ssh -i ~/.ssh/amd359 -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 \
  -L 127.0.0.1:18591:127.0.0.1:18590 root@DROPLET_IP
```

Build the working-tree router (`go build -o /private/router ./cmd/metrum-ai-router`).
Use a localhost port in 18400–18999, target the tunnel at
`http://127.0.0.1:18591/v1`, and register model `qwen-amd-ft` in the same
`support-agent` group. Caller/project scoping must leave other callers on teacher.
Use the existing example target capabilities/auth pattern; keep credentials in
private files, not argv. Prove file/tool execution plus a tool-result continuation
with `eval/live_smoke.py` before measurements. Then use `eval/load.py` against the
**router**, never the tunnel directly, at its fixed concurrency 1/8/32 profiles.
See each program's `--help` for token-file/model/output arguments.

Held-out wiring is blocked on #358's backend contract: re-execute new candidate,
teacher and base calls through the router with frozen replay initial state/tools,
not recorded teacher answers/tool results. Reuse the shared allowlisted backend,
not arbitrary code/URLs from exported rows. Keep failures in denominators, join
request IDs to usage/outcomes, run the same preregistered general regressions and
report per-arm quality/cost per successful task. Existing `eval/router_e2e.py`
is only a synthetic plumbing smoke, **not** held-out replay. Do not substitute
`eval/build_tests.py`'s old Glaive first-turn cohort for #358 multi-turn replay.

Report training tok/s/wall time, serving output tok/s/TTFT/p95/error rate at
1/8/32, cost/M output tokens and cost/success. Include host/setup/training/serve
lifecycle cost separately from fully utilized throughput extrapolations. Zero
successes means undefined/infinite cost per success, not zero. Compare against
NVIDIA only with matched export/checker/recipe and explicit hardware/engine,
merged-versus-LoRA and timing caveats. No parity values exist yet.

At every step end request `STATUS: GATE droplet-destroy`; the orchestrator must
verify deletion via API. Stop local router/tunnels too. Do not declare cleanup
complete merely because SSH stopped responding.

## Free local validation

```sh
python3 -m unittest discover -s examples/fine-tune-qwen3p8-27b/amd -v
bash -n examples/fine-tune-qwen3p8-27b/amd/first-boot.sh
bash -n examples/fine-tune-qwen3p8-27b/amd/capture-env.sh
```

These tests validate planning/configuration, not ROCm kernels or quality.
