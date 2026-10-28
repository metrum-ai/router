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

An authorized tool-only infrastructure-error rerun is being prepared using the saved adapter without retraining. Its results, when available, must identify the system-message normalization and remain separate from this original formal result.
