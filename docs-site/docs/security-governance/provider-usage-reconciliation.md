---
title: Provider usage reconciliation
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Provider usage reconciliation

The router can compare its prior UTC day's `request_usage` totals with provider
usage data and emit a report-mode security event when the provider total is
above the configured threshold. It never blocks inference. Enable the usage
database and security event store before turning on reconciliation.

```yaml
server:
  usage_db:
    enabled: true
  admin_reports:
    security:
      enabled: true
  provider_usage_reconciliation:
    enabled: true
    threshold_usd: 1.00
    timeout_ms: 10000
    max_pages: 20
    schedule_hour_utc: 6
    max_catch_up_days: 7
    providers:
      - provider: openai-primary
        kind: openai
        credential_env: ROUTER_PROVIDER_USAGE_OPENAI_ADMIN_KEY
        provider_key_id: key_abc123
      - provider: anthropic-primary
        kind: anthropic
        credential_env: ROUTER_PROVIDER_USAGE_ANTHROPIC_ADMIN_KEY
        provider_key_id: apikey_example123
      - provider: bedrock-primary
        kind: bedrock
        aws_region: us-east-1
        aws_role_arn: arn:aws:iam::123456789012:role/router-usage-read
```

`provider` must match a configured router provider. OpenAI and Anthropic entries
use a provider usage API credential from a `ROUTER_PROVIDER_USAGE_*`
`credential_env` and a vendor API key identifier. The dedicated prefix keeps
these high-privilege credentials separate from inference provider keys. Keep
the credential value in the deployment secret environment; configuration
stores only the variable name. The role used for OpenAI and
Anthropic usage analytics must have the vendor's read-only organization usage
permissions.

Bedrock entries use the AWS SDK's default credential chain, or assume
`aws_role_arn` when set. The role needs read access to CloudWatch metrics. The
configured AWS account and region are the reconciliation scope because
Bedrock's [runtime metrics](https://docs.aws.amazon.com/bedrock/latest/userguide/monitoring-runtime-metrics.html)
do not identify an IAM access key. Bedrock USD totals
are estimates: token counts are priced with the input and output rates in the
router provider model catalog. Include each Bedrock model used in that account
in the configured provider catalog so its usage can be priced; no more than 50
models may be configured per Bedrock provider.

Configure only one Bedrock reconciliation entry for each AWS account and
region. CloudWatch metrics are account/region scoped; adding multiple router
providers backed by that same account and region counts the same metrics more
than once.

The runner checks at `schedule_hour_utc` (default 06:00 UTC), after the prior
UTC day has time to settle. It records completed provider-days in the security
event store and catches up missed days after restart, bounded by
`max_catch_up_days` (default 7; maximum 30). Transient provider failures retry
up to two times with bounded backoff. When an API returns `data_refreshed_at`,
the runner skips a window whose watermark does not reach its end and emits a
partial-data event; the next scheduled run retries that day. Provider failures
and partial windows emit safe events. Logs/events exclude credentials,
response bodies and spend amounts; gap/failure events carry provider and window
in dedicated event fields. Provider API egress permits only HTTPS to
`api.openai.com` and `api.anthropic.com`, without environment proxies.

When started with reconciliation enabled, config reloads apply on the next
daily run; disabling pauses it and later re-enabling resumes it without a
restart. Enabling it after startup requires a restart. Each service replica
runs its own check, so a gap may produce one event per replica. Each process
permits one in-flight run. OpenAI and Anthropic pagination is bounded by
`max_pages`; API calls have `timeout_ms` bounds.

The event is emitted only when `provider_usd - router_usd` is strictly greater
than `threshold_usd`. This is a report signal; it does not suspend a provider
key or change routing. See the [security event overview](./overview.md).
