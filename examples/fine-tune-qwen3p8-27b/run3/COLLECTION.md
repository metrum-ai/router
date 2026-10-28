<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Run 3 collection protocol

This collector is separate from the source-plan freeze in
[PREREGISTRATION.md](PREREGISTRATION.md). It runs five scripted synthetic agent
personas through the **same agent-loop implementation** with five distinct
service-account caller keys and one uncaptured control key. This demonstrates
scoped multi-caller collection, not five independent agent frameworks or a
representative distribution of real customer traffic. Capture is enabled only
for the five `acme-support` callers; the `acme-it` control is not captured.
Every generated token is hashed with the reviewed catalog helper using
`routerctl callers hash`. The short-lived mode-0600 bearer-token file is in
private `/dev/shm`; only the CLI executable build uses `/tmp`. Raw keys and the
capture-encryption key live in process environment, never CLI arguments or the
output config. Loss of the process also loses the encryption key: interrupted
runs cannot resume or re-export; do not silently rerun an incomplete collection.

The full plan is verified against committed source and held-out hashes before
any request. An authenticated synthetic preflight for each key precedes the
fixed train → validation → test time barriers and is excluded from the export.
Eight workers send one attempt per task with at most eight turns; mock tools
are deterministic and only validated JSON arguments are executed. Failures
remain in the test denominator; no score-based refill, selective retries or
candidate input from teacher answers. Nine preselected uncaptured controls are
checked. Export is authorized and audited through the router, with project,
model-group and time filters, capture redaction, and a seven-day retention
setting. Train/validation filtering requires successful complete traces; test
retains all groups, including teacher failures. Completion requires 300/300
held-out task hashes and zero control capture/export.

The teacher is Fireworks `accounts/fireworks/models/gpt-oss-120b`; Chetan's
[synthetic-example-only decision](https://github.com/metrum-ai/router/issues/358#issuecomment-6366186842)
and terms/license hashes are included in the export request. This is **not**
a grant to train on customer data or an independent legal verification; users
must verify teacher and provider output-training rights for their own data.
[Fireworks model pricing](https://fireworks.ai/models/fireworks/gpt-oss-120b),
checked 2026-10-28, was $0.15/M input and $0.60/M output tokens. The script
refuses prices more than 31 days old; verify the provider's current rates and
the remaining per-pi $25 cap immediately before execution. Predispatch input
and maximum-output reservations stop at $19.50 and 9,400 attempts; these are
**not a hard provider invoice cap**. Known usage-derived costs and calls with
missing usage are recorded separately; provider charges for failures, retries,
taxes and invoices may be unknown. Reconcile before any retry. Previous run-2
spend is not included in run-3 cost.

Run only from a committed, reviewed collector with a signed source freeze, in
a private operator-owned directory. Prepare a worktree router binary and set
`FIREWORKS_API_KEY` in the private process environment. Never use actual
customer content. No cloud GPU or container is needed for this stage:

```sh
umask 077
uv run --with bcrypt --with jsonschema==4.26.0 python examples/fine-tune-qwen3p8-27b/run3/collector.py \
  --pool /secure/run3-pool --out /secure/run3-collection \
  --binary /secure/metrum-ai-router --execute
```

The output directory must not exist. It contains private router logs, encrypted
capture DB, request IDs, labeled traces and export files; do **not** commit or
upload it wholesale. Review and sanitize any aggregate evidence before storing
it in an approved artifact snapshot. Keep or delete the private state according
to operator retention; the live router is terminated after success or failure.
A SIGKILL/host loss may interrupt cleanup and leaves outcome/cost unknown; check
for processes and reconcile before retrying. The collector does not train, run
the original promotion gate, or authorize candidate promotion.
