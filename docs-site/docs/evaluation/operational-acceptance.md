---
title: Operational Readiness
doc_type: explanation
---

# Operational Readiness

Operational readiness proves that a Metrum AI Router deployment can be run, observed, updated, and rolled back predictably.

The checked-in `make launch-operational-readiness` target runs deterministic, secret-free repository contracts and writes a safe scalar JSON record under `tmp/`. That output is preflight evidence only. It does not prove the identity of a promoted artifact, a credential-backed deployment smoke, a rollback rehearsal, a named operational owner, or an approved public support commitment. Record those deployment-owned results separately and bind them to the exact artifact digest, environment, timestamp, and approver without publishing credentials or private endpoints.

## Release Readiness

Before rollout, confirm:

- binary release version is known;
- build timestamp is visible in `/version` and hosted docs;
- config has been validated with structured YAML parsing;
- provider keys and caller tokens are not present in the package or public docs;
- deployment-specific config backup path is recorded;
- rollback owner and rollback steps are known.

## Runtime Checks

Run:

- `/readyz`;
- `/version`;
- hosted docs page load;
- authenticated `/v1/models`;
- authenticated text request;
- required tool-call smoke;
- required image/VLM smoke;
- Codex CLI smoke for Responses-compatible agent workflows;
- Claude Code smoke for Anthropic Messages-compatible agent workflows;
- usage report check after traffic.
- if enabled, browser admin report smoke with browser-admin identity plus policy-based authorization, including `/admin/reports/`, `/admin/reports/api/summary?since=24h`, and `/admin/reports/export.md`.
- robustness smoke for fallback ordering, upstream timeout, provider `429`, caller limit rejection, and diagnostics redaction using mock or staging targets.
- outcome workload gate summary for changed model groups, including verifier/reward, client matrix, pass/fail thresholds, cost, latency, selected upstream distribution, and request/report correlation.

## Quota And Cost Checks

Validate:

- disallowed model groups return `403 model-not-allowed`;
- RPM/TPM/concurrency and traffic-shaping limits enforce as configured;
- daily, monthly, and lifetime budgets count usage as expected;
- request-time input/output/image cost fields populate;
- upstream-reported billed cost is stored separately when available;
- reports can group by caller, project, environment, model group, provider, model, status, cache, latency, tokens, and cost.
- browser reports, when enabled, show the same cost and performance dimensions without exposing raw prompts, token hashes, provider keys, or full config.

## Troubleshooting Checks

Confirm:

- caller-visible errors include request IDs;
- a 400 compatibility or modality error signals an unsupported dialect/tool/modality/cap requirement (see [Error Reference](../reference/errors));
- upstream timeouts, rate limits, and provider failures are distinguishable in diagnostics, with field meanings cross-checked against the [Diagnostics Schema](../reference/diagnostics-schema);
- request attempts and trace events are written without raw prompts, raw image payloads, raw tokens, token hashes, or provider keys.
- ordinary router caller tokens cannot access `/admin/reports/*` and receive `403 reports-forbidden`.
- domain-scoped report admins cannot read another project/environment request detail, while explicitly global `*` report admins can when that policy is intended.

## Post-Deploy Cleanup

After validation:

- remove uploaded packages from temporary host paths;
- remove superseded temporary unpack directories;
- keep intentional timestamped backups;
- confirm no stale `smart-llmrouter-*tar*` files remain in `/tmp`;
- run Docker prune when stale images/build cache/containers have accumulated and the current deployment is healthy;
- record cleanup results in the deployment notes.

## Rollback Criteria

Rollback or isolate a change when:

- health checks fail;
- required client smokes fail;
- a model group misses its defined quality criteria;
- a target violates capped-token behavior for capped requests;
- provider error or timeout rate exceeds the group threshold;
- usage or cost fields stop populating for affected traffic;
- security controls such as `/metrics` restriction or diagnostics redaction regress.
- browser report access stops requiring both browser-admin identity and policy-based authorization.
