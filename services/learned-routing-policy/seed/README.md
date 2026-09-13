# Seed corpus tooling (issue #157 iteration 1)

Implementation lives in the importable package
[`lrp/seed/`](../lrp/seed/) so `make lrp-test` mypy covers it.

## What this scaffolding provides

1. License audit recorder for
   `nebius/SWE-rebench-openhands-trajectories` at revision
   `35455389ab51bf5e2306bfd436ef72d0f98bf882`.
2. Pinned-revision download with sha256 verification. Operator data stays
   outside the repository (`lrp.collect.protected_path`).
3. Teacher-forced turn extraction. Replay prompts end at the last tool or
   user observation. The reference assistant continuation is stored
   separately.
4. Stratified sampling (N=1000 default) across turn-index, prompt-token,
   and turn-kind strata with session-disjoint `session_split` partitions.
5. Mixed portfolio target descriptors (OpenRouter Qwen + OpenAI GPT) with
   source-dated prices. No secrets in descriptors.
6. Replay driver wrapping `run_fanout` via the router (one group per
   target). Dry-run by default. Spend estimate aborts above 40 USD without
   an approval flag.
7. Verifier-class label columns and allowlisted sandbox plugins. Columns
   stay separate from training `quality` unless an operator applies the
   documented blend rule.
8. Parquet writer plus SHA-256 manifest helpers.

## Evidence limits

- Teacher-forced continuation agreement is not end-to-end task success.
- Tool name or argument mismatch is not by itself a failed task.
- Coding-trace evidence does not transfer to unsampled workloads.
- Cache pass rows are repeated observations, not independent turns.
- N=1000 is a practical budget, not statistical sufficiency.
- A public reference bundle is an example, not production readiness.

Paid replay is not executed by unit tests or by `execute=False` dry runs.
