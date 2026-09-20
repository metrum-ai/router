---
title: Troubleshooting
doc_type: howto
---

# Troubleshooting

Use this section to triage customer-visible failures without exposing secrets or private deployment details. Start from the caller symptom, collect safe evidence, then choose the fix path.

For a router that will not start because migration state is pending, running, failed, or incompatible, use the package's non-serving migration gate and release rollback class from [Upgrade Guide](/docs/release-notes/upgrade-guide). Authorized operators can inspect safe status through metrics-admin telemetry or the read-only **Operations / Data migrations** report; neither surface applies or reverses database work.

From Troubleshooting, you might be looking for the canonical downstream error contract and error catalog: see [Error Responses](/docs/reference/errors).

## Triage Flow

1. Capture the request ID, HTTP status, router error code, affected model group, client, and UTC time window.
2. Confirm the router is healthy with `/readyz`, `/version`, and `/v1/models`.
3. Use the symptom map below to choose the next page.
4. Open request evidence or usage reports with an authorized admin identity.
5. Apply the smallest fix: caller access, model-group config, quota, provider capacity, target eligibility, credential, or rollback.

## First Checks

```bash
export ROUTER_BASE_URL="https://<router-host>"
export ROUTER_TOKEN="replace-with-router-token"

curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/version"
curl -i -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

If `/readyz` fails, check config load errors, database connectivity, and required runtime files before testing individual requests.

If `/v1/models` does not include the expected model group, inspect caller access policy and model-group configuration. `/v1/models` is the caller-facing source of truth for allowed groups.

## Triage Map

| Symptom | Likely area | Next page |
|---|---|---|
| One request failed, timed out, or was slow | Request attempts, trace events, upstream provider path, quota, context, or fallback | [Request Troubleshooting](/docs/troubleshooting/requests) |
| `/readyz` fails | Config load, database connectivity, or required runtime files. License status is not a 3.0.0 readiness gate | [Software Licenses](/docs/legal/software-licenses) |
| Caller gets `401` or `403` | Missing caller token, disabled caller token, model-group access, metrics/report authorization, or admin policy | [Request Troubleshooting](/docs/troubleshooting/requests) |
| Caller gets `429` | Router quota, traffic shaping, TPM/RPM, concurrency, license volume/window, or upstream provider limit | [Request Troubleshooting](/docs/troubleshooting/requests) |
| `/metrics` returns forbidden | Caller is not authorized for metrics admin | [Observability](/docs/operations/observability) |
| Admin reports unavailable | Admin authentication, authorization policy, usage DB, or report feature license | [Admin Browser Reports](/docs/operations/admin-browser-reports) |

## Safe Support Packet

When escalating, include:

- router version and build timestamp from `/version`;
- exact UTC time window;
- request ID values;
- caller-visible error code and HTTP status;
- safe upstream fields when present, such as `X-Router-Error-Class`, `X-Upstream-Status`, `error.details.error_class`, and `error.details.upstream_status`;
- retry hints when present, such as `Retry-After`, `retry_after_seconds`, or `error.details.retryable`;
- requested model group;
- public token ID when available from reports;
- client name and version when relevant;
- safe license status fields when licensing is involved;
- a redacted summary of the workflow.

Do not include raw router tokens, provider keys, token hashes, raw prompts, raw images, raw tool outputs, full production config, private hostnames, SSH details, private signing material, or full customer-specific license payloads.
