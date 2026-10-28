<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# HumanEval three-mode live smoke (#330)

This is a 10-pair smoke, not statistical certification or completion of the
other #330 work. The same first ten pinned HumanEval test IDs, Inspect client,
seed, caps and Docker verifier were used for each mode. The offline
`api-compat-dynamic` matrix cell was not part of this live run.

| Cell | Attempts | Verifier passes | Wilson 95% CI | Paired wins / losses vs direct | Score delta vs direct | Regression gate |
|---|---:|---:|---:|---:|---:|---|
| direct | 10 | 10 | 0.7225–1.0000 | 0 / 0 | 0 | pass |
| fixed-router | 10 | 10 | 0.7225–1.0000 | 0 / 0 | 0 | pass |
| dynamic-group | 10 | 10 | 0.7225–1.0000 | 0 / 0 | 0 | pass |

All 30 attempts completed without recorded errors, router retries or fallback.
Allowed score regression was 0.05 and allowed error rate 0.10, unchanged.
`eval_report_schema.py` returned `eval report ok: 3 cells, schema valid, gates
pass`; the score-delta comparisons above were checked separately because the
schema script does not enforce them. Maximum observed first response was
7,911.451 ms and maximum task completion was 8,727.616 ms. There were no tool
turns; task latency, tool-turn and cost gates passed. The OpenAI-compatible
client made unary Chat requests, so time to first response is the model event's
completion minus its start; it is not a streaming-token latency measurement.

## Usage and costs

| Mode | Usage-derived model cost (USD) | Basis |
|---|---:|---|
| Direct | 0.004714800 | Provider-reported token counts multiplied by listed rates |
| Fixed | 0.004512204 | Router request-time usage rows |
| Dynamic | 0.003638808 | Router request-time usage rows |
| **Total model usage** | **0.012865812** | Sum above |
| Temporary VM | Not yet available | Resource-specific final invoice unavailable at report time |

These are usage-derived amounts, not a provider invoice; discounts, billing
adjustments and the final VM charge are not independently verified. The router
rows report `usage_source=reported`; they do not report an upstream dollar
amount. Direct usage had 1,628 uncached input and 3,522 output tokens, with
zero cached input tokens. Router modes together had 3,256 total input, 602
cached input and 6,126 output tokens. Standard Fireworks list prices used were
$0.30/M uncached input, $0.006/M cached input and $1.20/M output for both
DeepSeek targets ([model pricing](https://fireworks.ai/models/deepseek-ai/deepseek-v4p1-flash)).
The $10 limit included the VM and provider calls; serial requests, zero client
retries, 4,096 output-token cap and a conservative remaining-spend projection
were used. The VM's hourly price was $0.07143; this rate is not a final bill.

## Provenance and controls

Router main: `5cb00518fc6c6adcd94826999f4c67bce97a3629`; locally built image
ID `sha256:da57421f7aa6ca096239bf5baacd4199b43730b213d1a83f1f0e8c9b199a050e`.
Inspect AI 0.3.242, inspect-evals 0.15.0, OpenAI Python 2.54.0, Python
3.12.3, Docker 29.1.3, Compose 2.40.3. HumanEval dataset revision
`7dce6050a7d6d172f3cc5c32aa97f52fa1a2e544`; sandbox image digest
`sha256:bbaecf7a093cfe24703c97d562bb059899e272cd5d57f16d9916fe05e6614419`.
The verifier ran in Docker with networking disabled. A no-cost mock output was
scored incorrect in that sandbox. Seed 330 was recorded; it does not guarantee
determinism.

Direct and fixed targeted `accounts/fireworks/models/deepseek-v4p1-flash`.
The dynamic group used 70/30 weighted routing between that target and
`accounts/fireworks/models/deepseek-v4p1-flash-1017`. Both targets served
requests. The router listened on loopback, used SQLite request-time usage and
high caller limits, and recorded no local 429s. One run stopped after the first
fixed request because Inspect represents input tokens as uncached plus a
separate cached count while the router stores their sum. The recorded request
was reconciled and retained; the resumed runner did not repeat it. The ten
direct samples had zero cached tokens, so their initially recorded costs were
unchanged by the correction.

## Evidence and commands

`report.json` is the schema-v1 report; `results.jsonl` is a per-task,
content-free record and `router-usage-sanitized.jsonl` contains the 20 router
usage rows without request IDs, caller IDs or tokens. `trial-plan.json` records
the pre-call choices; `sandbox-compose.yaml` pins the sandbox and
`eval-config-sanitized.yaml` omits caller verifiers. No raw prompts, generated
code, model output, credentials or raw Inspect logs are included. The first
paid call and the two serial runner versions were invoked as follows, after
loading the Fireworks credential from the protected tmpfs file into the
process environment (not a command argument):

```sh
cd /root/inspect-empty
/root/eval-venv/bin/inspect eval inspect_evals/humaneval \
  --model openai/accounts/fireworks/models/deepseek-v4p1-flash \
  --model-base-url https://api.fireworks.ai/inference/v1 -M responses_api=false \
  --sample-id HumanEval/0 --sandbox docker:/root/sandbox-compose.yaml \
  --seed 330 --max-connections 1 --max-samples 1 --max-retries 0 \
  --max-tokens 4096 --token-limit 8192 --timeout 300 --log-format json \
  --log-dir /root/trials/direct-0 --display none --no-log-model-api
/root/eval-venv/bin/python /root/run-trials.py   # initial version, stopped on mismatch
/root/eval-venv/bin/python /root/run-trials.py   # reconciled resume version
python3 make-report.py results.jsonl report.json 2026-10-27T22:34:15.776891Z
python3 scripts/eval_infra_matrix.py
python3 scripts/eval_report_schema.py report.json
```

The two runner versions are preserved as `run-trials-initial.py` and
`run-trials.py`; the report-generation command uses the preserved
`make-report.py`. Run the last two validation commands from the repository root,
passing the report's actual path when it is not the current directory.

## Release note

Added verifier-backed three-mode HumanEval smoke evidence and sanitized usage
records; no router behavior or public API changed.

Metrum AI (pi agent)
