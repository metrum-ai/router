---
title: Evaluate Metrum AI Router
doc_type: explanation
---

# Evaluate Metrum AI Router

Use this checklist to evaluate Metrum AI Router in a self-managed deployment.
The goal is to verify client compatibility, governance, routing behavior, cost
evidence, and operational trust signals before rollout. For deployment shape,
start with [Deployment Patterns](../operations/deployment-patterns).

When the question is whether a routed group is as good as a fixed model or previous policy, use [Prove Router Quality](./prove-router-quality) to run an evidence-first comparison instead of relying on anecdotes.

## Protect Evaluation Inputs

Treat caller tokens, private endpoints, and protected usage settings as local
operator inputs. Keep completed environment files in an owner-readable runtime
location—specifically an owner-readable, package-local runtime location selected by the deployment—and load them without placing values on command
lines, reports, or public issues. Evaluation evidence should contain only
sanitized outcomes.

## What To Prove First

| Question | Proof point |
|---|---|
| Can existing clients integrate with minimal change? | Run OpenAI Chat, OpenAI Responses, Anthropic Messages, or SDK/CLI traffic by changing base URL, token, and model group. |
| Are provider keys and private endpoints hidden? | Confirm clients receive only the router endpoint, router token, and model group names. |
| Can the platform control who uses what? | Call `/v1/models` with different tokens and verify allow-list filtering. |
| Are tool and image requests routed safely? | Run one tool call and one image request through a group with validated compatible targets. |
| Does the router explain spend and performance? | Generate or review a report with provider/model, tokens, cost, latency, throughput, cache, attempts, and fallback fields. |
| Can model groups be tuned by outcome? | Run Harbor or another verifier and compare reward/pass status against cost and latency. |

## Self-Managed Evaluation Flow

1. Install an immutable release artifact in an isolated environment.
2. Configure operator-owned provider credentials and a caller token.
3. Validate production-like workloads and clients.
4. Inspect performance, quota, provider mix, security access, and retention
   evidence.
5. Record explicit promotion and rollback criteria before production use.

## Thirty-Minute Evaluation Path

1. Receive a router base URL, a caller token, and one or more allowed model groups.
2. Run [`/v1/models`](../getting-started/available-models) and verify only allowed groups appear.
3. Run an OpenAI-compatible chat smoke from the [API Quickstart](../getting-started/hosted-quickstart).
4. Run the client that matters most: [Codex CLI](../getting-started/codex-cli), [Claude Code CLI](../getting-started/claude-code-cli), or an OpenAI-compatible SDK.
5. Run one request-shape test: image input, tool call, quota/rate-limit behavior, or a deployment-specific policy route.
6. Ask the administrator for the usage/report excerpt for the evaluation window.
7. Confirm selected upstream provider/model, token counts, cost fields, latency, fallback behavior, and request status are visible without storing raw prompts or raw images.

This path should prove both caller-visible behavior and operator controls. The caller sees stable model-group names, compatibility with its chosen API shape, allow-list enforcement, and clear errors such as `model-not-allowed` or a 400 compatibility error; see [Error Reference](../reference/errors). The operator sees request-time evidence for target selection, cost, latency, attempts, fallback, and quota impact.

## Expected First Outputs

| Step | Successful result |
|---|---|
| `/v1/models` | JSON list of model group IDs allowed for the token. |
| Chat smoke | Assistant returns the requested short text. |
| Codex or Claude Code smoke | CLI returns the requested text or creates the requested disposable smoke file. |
| Image smoke | Model reads the image through the same router group or returns a 400 compatibility or modality error when configured targets cannot serve it. |
| Usage report | Report groups requests by caller, project, model group, provider/model, status, latency, tokens, cache, and cost. |

## Proof Package To Request

- One text request through `/v1/chat/completions`.
- One Responses request or Codex CLI task.
- One Anthropic Messages request or Claude Code task.
- One tool-call request using the API shape your client sends.
- One image request through the same group users would normally call.
- A `/v1/models` allow-list check for the evaluation token.
- A usage/report excerpt for the test window.
- A safe license-status summary when evaluating an enterprise or on-prem licensed deployment.
- A security summary covering provider-key handling, diagnostics redaction, metrics-admin isolation, and private-upstream network controls.
- A retention and rollup summary explaining raw operational rows, finalized daily rollups, legal holds, archived exports, and dry-run-only purge status.
- A rollback plan for disabling a target or reducing its weight.

## Outcome-Based Model Validation

Model routing should be evaluated by task success, not only by model reputation or token price. Harbor is one useful coding-agent harness, but any verifier that reflects real work can be used:

- unit tests for coding tasks;
- extraction accuracy checks;
- OCR target answers;
- tool-call correctness assertions;
- browser-control tasks;
- internal golden datasets;
- product acceptance tests.

The target state is a model group that preserves the required task outcome while improving cost, latency, reliability, or provider optionality.

For router-versus-fixed-model comparisons, pre-register the task set, control, metrics, seed policy, config version or safe routing/config summary, and rollout decision rule using the [Prove Router Quality](./prove-router-quality) template.

## Common Evaluation Failures

| Symptom | Likely meaning | Next step |
|---|---|---|
| `401` or `403` before any model call | Token or endpoint mismatch | Verify base URL and router token. |
| Expected group missing from `/v1/models` | Caller token is not allowed to use it | Ask the administrator to update access if intended. |
| 400 compatibility or modality error for tools or images | Configured targets cannot satisfy that request shape | Use a compatible group or add a validated target; see [Error Reference](../reference/errors). |
| Timeout on a long agent task | Upstream or per-attempt timeout is too small for the workload | Inspect request attempts and adjust target mix or timeout. |
| A cheaper group fails the verifier | The group does not satisfy that workload contract | Keep it for simpler tasks or promote a stronger target for that workload. |

See [Error Reference](../reference/errors) for structured error types.
