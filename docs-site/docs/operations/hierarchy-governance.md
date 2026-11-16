---
title: Hierarchy governance
doc_type: reference
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Hierarchy governance

A parent router can publish a small policy snapshot for each child router. The child fetches the
snapshot over HTTPS with its configured bearer credential and caches the validated response for a
bounded TTL. A loopback HTTP parent is accepted for local tests only. The policy contains an
organization and team identifier, the parent caller's quota, allowed model groups, tool names and
PII filter rules. It never contains credentials, prompts or provider data.

The parent maps each child to a caller. The child's inference credential must authenticate as that
caller at the parent, so the parent applies its normal group checks and quota admission to every
request it serves. A suspended parent caller may remain mapped so configuration reloads succeed;
authentication still rejects requests with that caller's credential. Child callers assigned to the
same organization must have identical day and month quotas; the parent combines their token and
request usage under a durable shared organization counter. The child's configured caller quota is
an independent local cap. Parent policy narrows group and tool access and unions PII filters. Parent
quota exhaustion is enforced when the request reaches the parent; the child does not mirror the
parent's live counter locally.

Use the same exact model-group names in the parent caller's allow list, the child policy mapping,
and the child router's model configuration. A name mismatch fails closed with a hierarchy model
group mismatch error. Names may contain ASCII letters, digits, hyphens, underscores, dots, and
colons (for example, `gpt-5.1`).

Example parent configuration:

```yaml
server:
  hierarchy:
    parent:
      children:
        - id: team-a
          caller_id: team-a-router
          organization_id: acme
          team_id: platform
          allowed_model_groups: [standard]
          tool_allow_list: [read_ticket]
          pii_filter:
            enabled: true
            mode: redact_only
            rules:
              - name: email
                expression: '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
                placeholder_prefix: EMAIL
```

The parent caller's day and month quota values define the budget the parent enforces when it serves
requests. If several child callers belong to one organization, configure the same quota values for
each. Use a different organization identifier to maintain a separate parent budget. The quota
status view for a mapped child caller reports the shared organization counters; rate limits and
lifetime key limits stay caller-specific. The child applies its own local quotas independently and
does not combine the parent's quota with its local cap.

Example child configuration:

```yaml
server:
  hierarchy:
    child:
      id: team-a
      parent_url: https://router.example.com
      credential_env: PARENT_ROUTER_TOKEN
      cache_ttl: 30s
      timeout_ms: 2000

providers:
  parent:
    base_url: https://router.example.com/v1
    dialect: openai-chat
    upstream_kind: router
    api_key_env: PARENT_ROUTER_TOKEN
```

The child uses the same secret for policy reads and inference. Store it in the deployment secret
manager and expose it only as an environment variable. Rotate it by updating the parent caller
verifier and the child secret together.

Set `trust_traceparent: true` on each parent caller whose credential authenticates the next router
in the chain. The setting lets that router accept the authenticated immediate child's W3C trace ID;
each router then propagates the same trace ID to its parent. Do not enable it for ordinary end-user
callers.

Within the cache TTL, a previously validated snapshot remains usable during a parent outage. After
the TTL, the child makes one shared refresh attempt; on failure, it may use the last validated
snapshot for up to 30 additional seconds. After that grace period, a failed, malformed, oversized or
timed-out fetch returns HTTP 503 before inference dispatch. The policy cache is process-local and
reload/restart drops it, so the next request must fetch a fresh snapshot. Only the parent charges its
organization counter for requests it serves. The parent's daily and monthly counters survive restart
in the configured state file, while in-flight reservations are process-local. Run one parent replica
for an authoritative organization budget unless the deployment has separately validated shared
quota storage.

The serving router exposes `GET /internal/v1/governance/policy`. It requires the mapped caller's
normal bearer authentication, returns `Cache-Control: no-store`, and has no browser or administrative
authentication path. Policy fetch requests include a bounded hop-depth header to prevent recursive
parent lookups.

The `caller_id` value must match a configured caller ID and use the safe identifier charset: 1–64 ASCII letters, digits, underscore, period, colon, or hyphen. Parent and child must use identical model-group names.

Usage schema version 22 stores `organization_id` and `team_id` beside the W3C trace and span IDs.
Each router records the organization and team scope it enforces. The admin request report returns
these fields as `organizationId` and `teamId`, along with `traceId`, `hopId` (the router's span ID)
and `parentHopId`. Filter `/admin/reports/api/requests` with `organization_id`, `team_id` and
`hop_id` to follow a request across the hierarchy. These are row-level filters; use raw history,
not finalized daily rollups.

## Governance test matrix

| Case | Expected behavior | Coverage |
| --- | --- | --- |
| Mapped child credential | Snapshot contains only the mapped child's organization, team, limits and policy | `TestHierarchyParentPublishesCallerBoundPolicy` |
| Unknown credential | Policy endpoint returns unauthorized | `TestHierarchyParentPublishesCallerBoundPolicy` |
| More permissive child groups/tools | Effective policy remains narrowed by the parent | `TestHierarchyPolicyNarrowingAndPIIUnion`, `TestHierarchyThreeRoutersInheritPolicyAndEnforceParentBudget` |
| Multiple team callers in one organization | Token and request quota is shared and persists across restart | `TestHierarchyOrganizationBudgetIsSharedAcrossChildCallers` |
| Parent outage before TTL | Last validated snapshot remains available | `TestHierarchyPolicyRuntimeCacheTTLAndFailure` |
| Parent outage after TTL | Last validated policy is used for at most 30 seconds, then refresh fails closed | `TestHierarchyPolicyRuntimeCacheTTLAndFailure` |
| Parent timeout | Inference returns 503 before upstream dispatch | `TestHierarchyParentTimeoutFailsInferenceClosed` |
| Parent budget exhaustion | Nested request is refused without another provider call | `TestHierarchyThreeRoutersInheritPolicyAndEnforceParentBudget` |
