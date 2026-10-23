<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run 2 (#355): governed agent traces, shared freeze released

**Full export and signed NVIDIA/AMD registration are frozen.** Dataset counts:
1186train/221val/300test; same export bytes/views on both accelerators. NVIDIA
fresh RTX PRO6000 VM bootstrap is underway at$2.19/h, GPUcap$150/stop$120/max4h
with independent watchdogs. Student quality/promotion results remain unmeasured.
The direct-download preparation at`2bfd8de018` is **superseded**, not training data.
Current`preregistration.json` authorizes only its hash-bound governed cohort;
never activate`preregistration-direct-download-superseded.json` to train.

## Required router-centric loop

Run licensed task seeds through a real teacher agent loop and deterministic mock
tools, with teacher-output training rights checked first. Capture only the opted-in
support caller/project (redaction/PII policy, retention); other callers stay
uncaptured. The #358 agent owns export authorization/audit/filtering, capture
proofs, temporal splits and the collection/mock-backend interface.

Teacher rights are CLEARED for this synthetic technical test by the maintainer:
https://github.com/metrum-ai/router/issues/358#issuecomment-6366186842.
The Fireworks-hosted teacher is `accounts/fireworks/models/gpt-oss-120b`;
`teacher_terms_decision` is preserved in export/derived manifests and dated evidence,
including pinned license/provider-terms hashes. This is NOT customer-data permission.
Users must choose a teacher whose license AND provider terms allow training on
outputs, and verify that before enabling export. Collection cap remains about$20.

PR361/368's full real-teacher export is validated: train405native/523general/
258letter, val74/100/47, test200/75/25. All300 heldout tasks, including98 historical
teacher failures, remain. Original1/3/3 engineering sample is excluded, not training
or qualification data. Shared registration is signed at commit`881e394461`.

FT consumes canonical chat/tool JSONL plus manifest, preserving complete native
assistant/tool-result continuations and provenance. **All training rows, including
general/letter-format anchors, must come from the router export.** Downloaded
Glaive/OASST may be licensed task seeds for collection, never a direct training
fallback. Request a substantial general-instruction majority and letter-format
examples in the captured mix; actual counts and immutable hashes are registered.
The old 5,667/500/300 cohort and hashes are archival preparation, not current splits.

Coordination proposal:
https://github.com/metrum-ai/router/issues/358#issuecomment-6365813193

### Consumer contract — matched by PR361's real-teacher engineering export

`router_export.py` implements `metrum-router-ft-export-v1` as a proposed handoff:

- `manifest.json`: source.kind=`router_capture`, export_id, filters,
  split_boundaries, audit_event_ref, teacher_terms.training_outputs_allowed=true,
  teacher_terms_decision with scoped reference and pinned terms/license hashes,
  privacy capture_opt_in/redaction_applied/pii_filter_applied=true, and
  splits.{train,val,test}.{sha256,count}.
- Each split JSONL envelope has canonical `messages`, `tools`, `provenance`
  (trace_sha256,task_sha256,captured_at UTC), `outcome`
  (task_success,tool_schema_valid), and a heldout `replay` descriptor.
- Hash/count verification, complete tool continuations, success-only train/val,
  strict chronological separation and cross-split trace/task/prompt isolation.
  Heldout failures are not discarded. Exporter must enforce capture consent,
  policy/filter semantics and dedupe; consumer attestations are not independent
  proof of consent or PII absence.

```sh
# CPU only, outside Git; no download, model loading, training or inference:
python run2/router_export.py --export /secure/router-export --out /secure/canonical
```

The adapter preserves envelope metadata in `replay-test.jsonl`, copies the original
manifest, strips metadata only in canonical training views and encodes tools as
opaque JSON strings for Axolotl, avoiding Arrow nested-struct mutation. It currently
emits **training_authorized=false** until contract confirmation/new preregistration.
Do not hand-edit that flag to bypass coordination. Actual format differences will
be resolved with the exporter, not silently coerced/dropped.

## Retained conservative recipe and real pre-checks

Rank8/alpha16, one epoch, LR1e-5, effective batch16, no quantization/packing,
BF16 pinned base, SDPA and TF32=false on both NVIDIA/AMD. Only q_proj,v_proj,linear_attn.in_proj_qkv/out_proj; no
MLP/gate/vision/lm_head updates. Keep original base template bytes and explicit
non-thinking mode. No hyperparameter/checkpoint search on consumed heldouts.

After useful-sized export and joint NVIDIA/AMD protocol agreement (teacher rights
already cleared), freeze a NEW registration containing actual
export/data/view/source/scorer/recipe hashes, mix/counts, source-group/time splits,
replay contract, sampling, arms and unchanged numerical gates **before training or
heldout inference**. `readiness.py` requires confirmed router-export provenance;
the frozen registration additionally requires actual base-format/template/cache
proofs before smoke/full training; a registration alone is not hardware evidence.

On the remote GPU host only: run the five synthetic base-template/token-prefix
fixtures; serve bare base (`MODE=base`) via official loopback vLLM/SSH tunnel and
working-tree router; require twelve disjoint base letter-format probes and actual
native continuation. Stop vLLM, preprocess the full actual export in Axolotl,
audit all input tokens against base HF rendering and native supervised markers
(including calls earlier in multi-turn traces), then hardware smoke/full training.
Training/weights/merge stay on the GPU host, never on the coding-agent machine.

## Replay evaluation and cost

`replay.py` supplies a backend-independent engine: start each arm from the frozen
initial system/user task, execute its **new** tool calls against an isolated/reset
trusted deterministic backend, return those results through the router and check
actual task success. Recorded teacher assistant/tool answers are NEVER candidate
continuations. Backend code is trusted/frozen harness code, not imported from a
row; row-provided shell/code/URLs must never execute. `run_replay.py` provides
bounded preregistered concurrency/turns, same-group caller-bound router dispatch,
frozen backend/source checks and private incremental metadata. It is dry-run by
default. Trusted backend and full-export hashes are frozen; actual trained-target
caller binding and qualification execution remain required. Native replay uses
EXPORTED masked initial inputs/oracles. Auxiliary JSON-text/semantic evaluation
uses private licensed SOURCE gold unchanged through`project_eval.py`; exactly two
tool descriptors have different masked/source oracle values. These representations
and endpoints are explicitly distinct, never normalized or silently substituted.
`reconcile_replay.py` joins a post-run authenticated router cost ledger and emits
aggregate candidate/teacher/base task economics. CLI examples (not rental or
inference permission):

```sh
python run2/run_replay.py --export /secure/router-export \
  --preregistration run2/preregistration.json --backend /secure/trusted_backend.py \
  --arm candidate --out /secure/candidate-replay.jsonl
# --execute remains blocked until a NEW confirmed export protocol is frozen.
python run2/reconcile_replay.py --export /secure/router-export --records /secure/candidate-replay.jsonl \
  /secure/teacher-replay.jsonl /secure/base-replay.jsonl \
  --ledger /secure/authenticated-router-costs.json --out /secure/task-cost-summary.json
```

Replay the SAME heldout captured task set for **candidate / teacher / base** through
`support-agent`, with caller-scoped target bindings. Other callers remain on teacher.
Retain transport/format/task failures in denominators; no best-of/retry selection.
`attach_cost` joins authenticated router usage records by request ID; missing costs,
timeouts or unknown prices are not zero. `summarize` computes all attempted routed
model cost / successful tasks per arm, including failures in the numerator; zero
success or incomplete cost coverage gives null, not fake free/infinite savings.
These are router-recorded cost estimates, not invoices or lifecycle ROI.

Native task success is NOT the old strict JSON-text exact score. Freeze how replay
and the original exact endpoint coexist before inference; do not silently rename
one into the other or relax the original ≥10pp gain/≤2pp general-regression,
no-pass/error-regression/p95≤1.25 gates. Same-item paired CIs/repeats and all FT arms
remain required. Public historical judge calibration60%(CI46–74%) is weak, not
in-house domain validation. Standard-cohort selection also awaits the new freeze.

## Post-registration merged parity addition

The maintainer requested an **additional NVIDIA BF16 safe-merged arm** after
ROCm vLLM could not load AMD's unmerged LoRA. `parity-addition.json` records its
scope before merged inference. The registered NVIDIA base+LoRA remains the gate
arm, unchanged; merged results cannot substitute for or rescue that verdict.
Use the same final adapter and pinned base with the exact AMD
`standalone/merge.py` procedure (`device_map=cpu`, BF16,
`merge_and_unload(safe_merge=True)`), on the remote GPU host only. Record each
weight-shard SHA and the documented aggregate, actual merge package versions,
and all frozen quality/load/control measurements through the same group with a
fresh caller binding. No merged results or equality are claimed until measured.

Compare NVIDIA LoRA versus merged with complete paired cohorts; any gap is a
finding. NVIDIA-merged versus AMD-merged aligns serving representation, but the
independently trained adapters and different trainer/kernel/runtime stacks
remain confounders—not pure hardware causality. Prioritize the registered arm
within the existing lease's hard deadline; report any unmeasured parity scope.
No extension, replacement cohort, output normalization, or relaxed gates.

## Rental/cleanup only after export and freeze

Recheck authenticated approved GPU inventory and choose the cheapest fitting
Docker VM (≥500GB disk, compatible driver, requested ≥1Gbps network). Never
re-rent idly on authentication failure: **authenticate first, then retry**.
The lease helper's explicit `--budget-profile run2` enforces $120 stop/$150 cap,
maximum4h; reserve vendor/judge costs via `--reserved-other-usd` (planned70).
Dry-run is default; paid creation requires `--execute`. Independent bounded
watchdogs and exact API verification of instance/key/vendor teardown are required.
Provider ID `vllm_onprem_ft` is hardware-neutral. Run1 actually trained on RTX PRO
6000 Blackwell Server Edition; never infer B200 from an old alias.

Run-2 PR: **Refs #326 / Fixes #355**, dependent on #358; no promotion/production claim.
