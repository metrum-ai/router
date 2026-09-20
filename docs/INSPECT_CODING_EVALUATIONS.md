# Inspect coding evaluations

Issue #562 adds an explicitly invoked coding-evaluation lane. It is not part of `make`, `make test`, or any normal build because it needs a live model endpoint, an authenticated caller, Docker, and a bounded spend approval.

Copy [`examples/inspect-coding-evaluation.env.example`](../examples/inspect-coding-evaluation.env.example) to `examples/inspect-coding-evaluation.env`. That exact filled-file path is ignored by the repository; set it owner-readable only (for example, `chmod 600 examples/inspect-coding-evaluation.env`) before loading it through the protected runtime environment. Never put a caller key on a command line or in a checked-in file.

## Ordinary bounded evaluation (no usage DB input)

The ordinary mode is the default: leave `EVAL_REQUIRE_REASONING_COVERAGE` unset or `false`. It needs only the router endpoint, caller key, caller-visible model group, bounded limit, and local Inspect/Docker prerequisites. It does **not** need `EVAL_USAGE_CONFIG_YAML`, `EVAL_USAGE_CONFIG_FILE`, or `EVAL_USAGE_CALLER_ID`.

Load the completed ignored copy in the shell that will run the evaluation, then run one bounded suite:

```sh
set -a
. ./examples/inspect-coding-evaluation.env
set +a

make eval-humaneval
# Or: make eval-bigcodebench
```

An ordinary run writes the sanitized Inspect aggregate. It intentionally reports reasoning usage as `not reported` unless a separately created aggregate-only coverage file is supplied to `make eval-report`. For router-group cost comparison, `eval-report` also needs the pre-existing safe request-time usage aggregate documented by the CI workflow; it is not part of the ordinary bounded-run prerequisite set.

`EVAL_MODEL_KIND` is required policy context: use `router-group` for a caller-visible deployment group and `direct-baseline` only for an exact model listed in `approved_direct_baselines` in the evaluation policy. It is never inferred from a slash because group names are deployment-defined strings. `EVAL_API` selects the Inspect model provider (default `openai`) and prefixes `EVAL_MODEL`; `EVAL_REASONING` is passed as Inspect's `reasoning_effort` model argument. `EVAL_CONCURRENCY`, `EVAL_TIMEOUT`, `EVAL_INSPECT`, and `EVAL_POLICY` are explicit overrides. Limits are 1–200 and concurrency 1–16. Use the caller output-cap, dialect, tool mode/count, streaming state, and request-size bucket intended for promotion as separate evidence; a text-only OpenAI-style suite does not validate Responses, Messages, or bridge traffic.

The wrapper executes Inspect from an empty disposable directory. This prevents Inspect from auto-selecting this repository's Dockerfile as its code-execution sandbox. Each Make invocation creates an isolated timestamp/PID `EVAL_LOG_DIR`, shared by its explicitly requested suite and report targets; CI pins the run ID across separate Make calls. The exporter uses Inspect's supported log API and reads only headers and sample summaries into `inspect-aggregate.json`; only `evaluation-summary.json` and `.md` are safe to upload. They omit prompts, responses, schemas, raw logs, credentials, headers, and token hashes.

## Coverage-required evaluation (protected and fail-closed)

Inspect cannot report router reasoning usage itself. Set `EVAL_REQUIRE_REASONING_COVERAGE=true` only in a protected environment when reasoning coverage is required. That mode requires a dedicated evaluation caller in `EVAL_USAGE_CALLER_ID` and **exactly one** protected router usage configuration source: `EVAL_USAGE_CONFIG_YAML` (a CI secret written to an owner-only temporary file) or `EVAL_USAGE_CONFIG_FILE` (an owner-readable local file). Missing the caller, providing neither source, or providing both sources blocks before evaluation; it never degrades to an unverified ordinary run.

`make eval-ci-smoke` and `make eval-ci-full` set coverage-required mode themselves and retain this fail-closed privacy contract. The reusable wrapper derives the suite UTC start/end window, filters by the dedicated evaluation caller and `EVAL_MODEL` resolved group, and optionally filters `EVAL_USAGE_CLIENT`. It never puts database credentials on a command line.

The exporter writes only totals plus provider/model/dialect attempt aggregates—never request IDs, caller IDs, prompt/response content, headers, endpoint hosts, or credentials. `EVAL_REASONING_COVERAGE_FILE` remains an explicit owner-readable override for an already-created aggregate-only export when using `make eval-report` outside the CI flows.

The exporter writes a new owner-readable (`0600`) file even if its output path already exists. The wrapper validates any configured export before reporting: all four nonnegative aggregate counters and the provider/model/dialect rows are required, each row must be internally valid, and row totals must exactly match the aggregate counters. A malformed or inconsistent export blocks the report rather than degrading to `not reported`.

Suite bounds retain nanoseconds. The exporter adds a bounded terminal-persistence grace period (default two seconds) after the suite finishes; configure `EVAL_USAGE_EXPORT_GRACE_SECONDS` only within 0–30 seconds. `--usage-db-config` preserves its configured usage-database migration policy, so protected read exports do not fall back to implicit schema changes. When diagnostics intentionally suppresses attempt child rows, the export marks provider/model/dialect coverage incomplete while retaining the request-level totals; consumers accept those bounded partial rows without treating missing diagnostics as inconsistent telemetry.

```sh
# Run in the protected environment that can read the usage DB. Keep the output
# outside the repository and make it owner-readable only.
go run ./cmd/metrum-ai-router-usage-report \
  --usage-db-config /protected/router-config.yaml \
  --from 2026-07-23T10:00:00Z --to 2026-07-23T11:00:00Z \
  --caller-id dedicated-evaluation-caller \
  --resolved-group deployment-defined-group \
  --reasoning-coverage-out /protected/eval/reasoning-coverage.json

EVAL_REASONING_COVERAGE_FILE=/protected/eval/reasoning-coverage.json \
  make eval-report EVAL_SUITE=humaneval
```

If that explicit file is unavailable or malformed, reporting blocks rather than silently presenting unverified coverage. In ordinary mode with no file configured, the report deliberately says `not reported`; it does not infer reasoning values from Inspect logs. Do not copy the protected exporter file into the repository or CI report directory—the report sanitizer copies only its allowlisted scalar fields into `evaluation-summary.*`.

`make eval-ci-smoke` is a two-task router-group smoke for an explicitly dispatched protected workflow. `make eval-ci-full` is the reusable protected full-lane command: it requires protected router inputs, one sanitized baseline aggregate per suite, and one sanitized request-time router-usage aggregate per suite. Router-group cost growth is calculated only from the latter's `stored_request_time_cost_usd` scalar, exported from persisted router usage reporting—not from evaluator estimates. It runs HumanEval and BigCodeBench in separate directories and creates timestamped sanitized reports. Empty or incomplete protected baselines fail closed, as do partial, skipped, blocked, or otherwise incomplete candidate aggregates. The workflow pins the reviewed Inspect harness versions and is only an environment adapter and trigger for this command. Promotion review remains human-controlled. Roll back by removing the candidate from the group or reducing its weight; never promote on this benchmark alone.

When the protected manual workflow invokes `make eval-ci-full`, it sets `EVAL_SAVE_CI_REPORT=true`. The target copies only the sanitized JSON aggregate and detailed Markdown summary to the versioned report directory `docs/evaluation-reports/inspect/<timestamp>/`. Timestamps are supplied by CI and preserve an immutable run history; do not put raw Inspect logs, prompts, outputs, headers, credentials, or token material there. The workflow uploads those sanitized files as artifacts and does not push them to the repository.
