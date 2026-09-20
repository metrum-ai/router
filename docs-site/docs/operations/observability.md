---
title: Observability
doc_type: explanation
---

# Observability

Metrum AI Router exposes operational signals for health, readiness, metrics, logs, usage reporting, and request-level diagnostics. These surfaces are designed to help operators answer two questions: whether callers are receiving reliable service, and which upstream provider/model paths explain latency, cost, errors, or fallback pressure.

## Surfaces

| Surface | Path or tool | Access model | Use |
|---|---|---|---|
| Health | `/healthz` | Deployment-controlled network access | Process liveness. |
| Readiness | `/readyz` | Deployment-controlled network access | License, config, and dependency readiness. |
| Version | `/version` | Deployment-controlled network access | Safe release version and build timestamp. |
| Metrics | `/metrics` | Caller subject with metrics authorization | Prometheus-style operational telemetry. |
| Usage reports | `router-usage-report` | Secure admin shell with DB access | Markdown usage, cost, latency, throughput, and rollup reports. |
| Browser reports | `/admin/reports/` | Admin authentication plus authorization | Authenticated cost, performance, cache, fallback, security, and drilldown views. |
| Request diagnostics | usage DB child tables | Admin/report authorization or DB access | Request attempts, trace events, terminal errors, request-shape and translation-shape telemetry, traffic-shaping events, and optional decision telemetry. |

Ordinary application caller tokens must not receive `/metrics` or admin report data. They should receive `403 metrics-forbidden` or `403 reports-forbidden` when they are not authorized for those surfaces.

## Request IDs

Every response includes `X-Request-Id`. Structured error bodies also include the request ID. Use it as the join key across logs, usage rows, attempts, trace events, terminal errors, and admin browser drilldown.

```bash
curl -i "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "replace-with-allowed-model-group",
    "messages": [{"role": "user", "content": "Reply OK only."}],
    "max_tokens": 16
  }'
```

Preserve the `X-Request-Id` value when opening support tickets. Do not include raw prompts, images, tokens, provider keys, or full config files in tickets.

## Metrics

Configure a dedicated metrics-admin caller and restrict `/metrics` to that subject. Metrics are global operational telemetry, not a tenant-scoped caller endpoint.
Rejected or unknown caller-supplied model names are collapsed to bounded labels such as `rejected_model`; authorized model groups keep their configured group labels.

Migration health is also aggregate-only and emitted only to the same metrics-admin surface: schema/data version, compatibility, pending definition count, and bounded data-job state count by persistent scope. When a migration-ledger status lookup is temporarily unavailable, the authorized scrape still returns the existing operational metric families and sets `smart_llmrouter_migration_status_available{scope="usage"}` to `0`. It has no caller, token, tenant, prompt, SQL, DSN, or error-text labels.

Example check:

```bash
curl -i -H "Authorization: Bearer $METRICS_ADMIN_TOKEN" \
  "$ROUTER_BASE_URL/metrics"
```

Expected for an ordinary application caller:

```bash
curl -i -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/metrics"
```

Response: `403 metrics-forbidden`.

## Logs

Router logs should be collected by the deployment logging system and retained under the customer's operational policy. Logs are for metadata and diagnostics; they must not contain raw bearer tokens, provider API keys, token hashes, raw prompts, raw images, raw tool outputs, full upstream headers, or full config files.

Useful log dimensions include:

- request ID;
- caller-safe identity fields;
- requested model group;
- selected provider/model/dialect;
- status code and error class;
- latency and upstream attempt count;
- fallback state;
- cache state;
- safe license status;
- safe quota or budget outcome.

## Usage And Performance Reports

Use `router-usage-report` or `/admin/reports/` to separate downstream caller experience from upstream provider behavior.

```bash
router-usage-report \
  --driver sqlite \
  --db /app/state/usage.sqlite \
  --since 24h \
  --out usage-24h.md
```

Start investigations with:

- errors and fallbacks;
- downstream latency and throughput by caller, project, model group, and client;
- upstream duration, TTFB, throughput, and error rate by provider/model/dialect;
- quota, TPM/RPM, context, and max-token troubleshooting buckets;
- recent request drilldown by request ID.

For report details, see [Usage Reporting](./usage-reporting) and [Admin Browser Reports](./admin-browser-reports).

## Migration Visibility

Migration status is operational telemetry, not a write surface. A metrics-admin caller can inspect aggregate migration health through `/metrics`; an authenticated browser administrator with `admin:reports` `read` can inspect the read-only **Operations / Data migrations** report. Both expose safe scalar state only. For a migration bound to a data job, the ledger remains authoritative: `failed` or `running` ledger state takes precedence, and job state refines only an applied ledger row. Use the non-serving deployment-job procedure in [Upgrade Guide](../release-notes/upgrade-guide) for plan, apply, recovery, or rollback.

## Alerting Checklist

At minimum, alert on:

- `/readyz` failure;
- elevated 5xx responses;
- elevated router-side `429` quota/rate-limit responses;
- elevated upstream provider 429/5xx attempts;
- fallback rate above the deployment baseline;
- request latency above the deployment service target;
- metrics scrape failures;
- usage database write failures.

Tune thresholds by workload. Developer tools and agentic clients can produce large-context bursts that look different from chat or extraction workloads.

## Troubleshooting Entry Points

- [Request Troubleshooting](../troubleshooting/requests) for one failed or slow request.
- [Operational Troubleshooting](../troubleshooting/) for install, routing, quota, metrics, and provider issues.
- [Software Licenses](../legal/software-licenses) for the 3.0.0 licensing removal. Runtime `license-*` errors are no longer emitted.
