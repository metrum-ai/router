# Run 3 frozen gate: formal failure and transport limitation

The unchanged, preregistered gate **failed** on `tool_exact_improvement_below_10pt` ([outcome.json](outcome.json)). This is not a clean comparison of tool quality: **all 200 tool cases in each arm failed at the transport layer**, before a model tool outcome was observed. The original result is retained and is not replaced by a later infrastructure-error rerun.

The Qwen server logged 402 HTTP 400 responses and `System message must be at the beginning` warnings; this log count includes requests beyond the 400 scored tool cases. Router telemetry for the 400 tool cases records HTTP 502; the server rejected their chat template. The frozen builder prepends a system instruction to an already-present system message, producing `system, system, user`. In a synthetic local reproduction, the real router forwarded that sequence unchanged to a Qwen-style enforcing mock. Thus the observed defect is in the gate request builder for this path, not router translation. Joining the two system texts in order allowed the synthetic request to pass, but changes the wire representation and was **not** the frozen gate run. Reproduction snapshot: `3bd6607a`; normalized router-path test: `7f92f454` (`router/v45-proof`).

| Frozen regression | Base | Adapter | Difference |
|---|---:|---:|---:|
| IFEval | 163/200 | 163/200 | 0 pp |
| MMLU-Pro | 122/200 | 121/200 | −0.5 pp |

These regressions stand; neither was rerun. See [IFEval](ifeval.summary.json) and [MMLU-Pro](mmlu-pro.summary.json). The original quality summary and all three prescribed sampling summaries are retained alongside this report. The separate scalar gate reported pass, but both arms had high and identical transport error rates; it does not establish tool quality or override the formal fail.

## Training, serving and provenance

- Frozen selected plan SHA-256 `3310f9cb2d983f72e20cd6a9b3c5489b36758468b30ac00f3b7a0f8411bb1a55`; frozen gold `00e09198e5572487764a768619d48c6b7083e48a69e98d992501d861b4362483`; frozen gate `4e0451e539de91ac6411c67ad8947f26e52ec95ef9dcac6616fed50d0d55e9b6` (snapshot `11f249e2`).
- The pinned recipe completed 187/187 training steps; reported final loss was 0.4658. The exact trainable-parameter inventory was 13,238,272. Single-H100 serving used the pinned vLLM image and `--max-num-seqs 128`; base and adapter registered and passed health checks.
- Three direct SCP attempts for the adapter failed with permission denied. A bounded read-only `sudo -n` fallback succeeded; host and local checksums matched. Adapter weights SHA-256: `40dd0512e702476564b5f863f072f46413aab90e59bbbe14e5fadddd7a7a7ed0`. Adapter and aggregate gate evidence: snapshot `aa354e18` (`router/v45-proof/425-run3-final`); server transport logs: `d7160d5d`. Raw held-out prompts and model outputs are not published here.
- The completion barrier preceded teardown. The instance, exact provider key and local keys were independently verified absent. Provider `cost_estimate` was $7.1823922862 for this instance; judge usage-derived estimate was $1.2424275. Neither is an invoice, and lagged charges are not included.

## Tool-only infrastructure-error rerun: incomplete

A single-H100 rerun used the checksum-verified saved adapter, **without retraining**. It selected the original 200 frozen tool cases per arm, retained their labels and scorer, and joined the two leading system texts with two newlines at the local upstream boundary. This changes the wire representation; it is not a replacement for the original formal gate. No regression or sampling cohort was rerun, and no hosted judge was used.

| Per arm | Count |
|---|---:|
| HTTP 200 model responses | 26 |
| HTTP 502 `upstream-failed` | 8 |
| HTTP 503 `quota-state-error` | 166 |

The rerun attempted all 200 cases per arm, but **348 of 400 tool cases failed in transport**. Model responses were observed for 52 cases, unlike the original run, where all tool outcomes were unobserved. A complete telemetry join failed because request IDs were absent from the copied usage DB. Consequently, the rerun is **incomplete**, does not establish a valid tool-quality comparison and does not establish whether the +10-point criterion was met. The on-host partial summary scores missing outputs as zero and must not be interpreted as model quality.

After the failure the local 64 MiB `/dev/shm` was full; the normalizer logged `no space left on device`, and the router returned `quota-state-error`. The prior combined evaluation state occupied 56 MiB. Pre-lease validation did not check free space on this shared tmpfs; the exhaustion is a strong infrastructure explanation, not a proven account of every individual error. No cases were retried. [Sanitized result and audit](tool-rerun-incomplete.json); partial aggregate and safety evidence snapshot `dbd3e554` (`router/v45-proof/425-tool-partial`). The instance and exact provider key were independently verified absent, as were local SSH keys (teardown proof snapshot `388547bf`). The last observed provider `cost_estimate` before teardown was $1.0886097082, not an actual charge or final invoice. Mac authorized one further H100 lease only after moving local state and temporary output to workspace disk and successfully replaying all 400 tool requests against a local mock.

## Second tool-only infrastructure-error rerun: complete

The full 200-case × two-arm local mock replay completed before the second lease: 400 joined tool responses and two synthetic admission probes all returned HTTP 200, with no 5xx. Local state and temporary files were on workspace disk; the mock is infrastructure validation, not model-quality evidence (snapshot `fb9cc376`). The same saved adapter was loaded on one H100 without retraining. The original cases, labels and scorer were retained; as above, the shim joined the two leading system texts with two newlines. No regression or sampling cohort was rerun and no hosted judge was used.

| Frozen tool exact-match score | Base | Adapter | Adapter − base |
|---|---:|---:|---:|
| Normalized infrastructure-error rerun | 0/200 (0%) | 75/200 (37.5%) | +37.5 pp |

The paired 95% interval reported by the frozen summarizer is +31 to +44.5 pp. The rerun **meets the +10 pp tool criterion**. It does not produce a new overall gate result or replace the original formal failure. The first incomplete infrastructure rerun had already exposed 26 cases per arm to model inference; this second attempt is therefore a disclosed repeated held-out exposure, not an independent preregistered run. For context, the rerun's `json_schema` metric was 142/200 base versus 77/200 adapter; exact-match improvement does not imply improvement on every metric.

All 400 scored responses joined to distinct router usage records with HTTP 200 and the intended base/adapter targets. The shim audit shows 400 normalized tool requests plus two admission probes, all HTTP 200, and the frozen scorer reported zero skipped callbacks. See the [aggregate summary](tool-rerun.summary.json), [sanitized result](tool-rerun-complete.json), and [shim audit](tool-rerun-normalizer-audit.json). Host/local hashes were verified; the evidence was saved before teardown as snapshot `7d4560d0` (`router/v45-proof/425-tool-final`). The last observed provider estimate for this second instance was $1.0690013774, not an actual charge or final invoice. The original IFEval and MMLU-Pro results and the original formal failure remain unchanged.
