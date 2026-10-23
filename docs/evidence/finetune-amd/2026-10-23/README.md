<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# AMD Instinct #359 — measured, original quality gates FAIL

**Do not promote.** The fixed trained AMD checkpoint served successfully, but
original JSON-text exact gain was **−1pp**, not the required **+10pp**. Overall
primary pass also regressed. Native tool API success is a separate measurement
and cannot replace these gates. No checkpoint search, held-out tuning or
score-driven inference rerun occurred. PR readiness means reviewable evidence,
not acceptance, promotion or permission to merge.

## Frozen data and runtime

`shared-freeze.json` identifies the exact governed #358 export (1186/221/300),
byte-identical derived views and supervised-token totals shared with NVIDIA.
`qualification-preregistration.json` is exact shared **e411fc9f…**; the training
parent96 remains provenance, not a mixed qualification registration.
`qualification-amendment.md` discloses the excluded294episode partial96native
run, retained with costs. No primary/regression/repeat inference ran under96.
The duplicate-declaration case stays in every full denominator and fails without
dispatch. No training/native fixture restored masked private data.

Official ROCm PyTorch BF16 passed. Axolotl was attempted first but its pinned
Torch requirement conflicted with the official ROCm image; no CUDA Torch was
substituted. Documented PEFT fallback passed actual two-step smoke and fixed
one-epoch75step training:1214.947s,652.448inputtok/s,63,341,102,592bytes peak
allocated. These are training counters, not quality. AdapterSHA
`9d78ff96de0c0ee9a8c00f894689de78824d090b9aa3d6134f4cba2bef6f8b85`.

Unmerged vLLM LoRA failed (`NoneType.shape`); unchanged shared CPU/BF16
`standalone/merge.py` safe merge succeeded. `merged-weight-evidence.json`
records exact command, versions, source and12-shard manifest hashes. Serving is
**merged BF16**, not unmerged. Actual working-tree localhost-router chat, SSE,
JSON-schema and file/new-tool-result continuation passed. GPU was never public.

## Quality: same-stack ROCm base versus AMD merged

Full paired scalars, fixed-scorer bootstrap CIs and authenticated coverage audits:
`qualification-results.json`; machine-readable verdict in `gate-verdict.json`.
`native-results.json` adds native paired CIs and explicitly quoted whole-window
GPU cost per native success (not semantic accuracy or healthy-teacher ROI).
All candidate/base HTTP qualification joins are200;
known duplicate-declaration no-dispatch failures remain in the denominator.

| Measurement | Base | AMD | Verdict |
| --- | --- | --- | --- |
| Original JSON-text exact,200tool cases |9/200 (4.5%)|7/200 (3.5%)|−1pp; paired95%CI[−2.5,0]pp; **FAIL** required+10pp |
| Overall primary pass,300 |86/300|80/300|−2pp; **FAIL** no-regression |
| IFEval fresh200 |161/200|164/200|+1.5pp; <=2pp regression guard passes |
| MMLU-Pro fresh200 |124/200|124/200|0pp; guard passes |
| Primary p95 provider latency |34.302s|34.487s|1.0054x; <=1.25 guard passes |
| Native tool task success,200 |180/200|180/200|90% both; does not rescue original gate |
| Native all300 |259/300|262/300|Includes letter9→12/25, general completion70/75 both; NOT semantic accuracy |
| Non-tool sampling100, seeds326/327/328 |75/72/74%|82/78/75%|Same100cases repeated, NOT300independent tasks |

Frozen SDK response-error metric is0/300each, with a separately disclosed
client schema/predispatch failure1each. The strict SDK journal guard exited2
for primary299HTTP IDs/300rows. Metadata-only `amd/qualification.py` proves
299actual joins plus1known no-dispatch; it never fabricates IDs or drops cases.
The absent objective callback receives unchanged frozen scorer zero (exactn200).
Final base seed328 subprocess initially could not launch; zero requests, no
output. `sdk-runtime-availability.json` was signed before completing the entire
previously unstarted100cases with the same restored locked executable.

## Load, controls, economics and limitations

Corrected high-capacity32request load profiles at concurrency1/8/32:
70.533/395.608/1400.613outputtok/s, zero errors. End-to-end p95latency:
1.869/2.684/2.917s; streamed p95TTFT:152.8/232.4/272.3ms (same1/8/32order).
Fully utilized quoted GPU cost/M
output tokens:14.9654/2.66819/0.753638USD. These128-token-cap profiles are not
quality-equivalent economics or invoices. Initial26local concurrency429s were
retained as invalid; **all three profiles** reran before held-out inference.
All current eval callers had64concurrent/20kRPM/100Mtpm/day20krequests; no extra
project/group throttle. Example generator defaults are now high as well.

Scalar evidence is in `router-controls.json`. Same support-agent group real-file plus newly generated receipt continuation
passed base/candidate/teacher/other-project,8requests with authenticated targets.
Quota and revocation deliberately deny before upstream (**intentional limit
tests**, not quality traffic). Provider-agnostic rollback to teacher passed.
An initial GPU-specific payload routed to teacher but got upstream400→502;
retained, not concealed. External strategy **does not enforce dynamic-strategy
spend_ceiling**: priced-teacher control returned200 even with tiny ceiling.
This is an explicit limitation, not a passed spend guard. Zero token-price
metadata never bounds GPU rent; independent physical deadline is mandatory.

Shared teacher-native e411 was degraded by227router RPM429 episodes. That run
is **unusable for healthy-teacher quality or cost/success comparisons**. FT owns
the authorized signed infrastructure-only whole300teacher replacement; AMD does
not duplicate it or wait with rented hardware. NVIDIA matched merged results
remain a separately owned comparison; do not headline a pure AMD/NVIDIA speed
or quality advantage from different toolkits, independently trained adapters,
versions and caller capacities. No healthy-teacher ROI/promotion claim.

`economics-and-teardown.json`: approximate orchestrator billing17:47–19:23Z,
1.6h×3.80USD≈**6.08USD** GPU; conservative17:45start estimate6.2067USD.
Judge800calls estimate2.8738975USD; successful engineering teacher11calls
0.0004917USD. Known estimate≈8.9544USD (conservative≈9.0811), **plus unknown
failed-upstream charges**, not a settled total/invoice. All bootstrap, idle,
invalid-load, aborted96 and failed-engine time remains in the lifecycle window.

## Teardown and permission

GPU work ended19:21Z, all containers and tunnels stopped. Orchestrator confirmed
**19:23Z DigitalOcean API404**, no droplets under tagamd359, and firewall
amd359-ssh-only plus cloud SSH keyamd359-20261023 absent. This receipt is operator
attestation from API verification, not independent AMD API access. No rerental.

Teacher permission is limited to synthetic Acme/mock-tool technical testing:
[maintainer decision](https://github.com/metrum-ai/router/issues/358#issuecomment-6366186842).
Keep export `teacher_terms_decision`; users must verify **both model license and
provider output-training terms**. No general customer-output authorization.

## Checks actually run

`make test-fast` passed after main integration (initial600s timeout retained,
complete1200s-bound rerun0); `make ft-example-test`, docs QA, secret/header/diff
checks passed. After high-capacity example fixes, `make ft-example-test` and
actual `eval/router_e2e.py --router-bin /workspace/router-v430/amd-local/router`
passed again, including real file creation, tool continuation, weighted/shadow,
privacy and explicitly labelled intentional quota denial. Detailed commands and
final publication checks are in `/workspace/router-v430/reports/amd.md`.
