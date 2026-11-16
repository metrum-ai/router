---
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
title: Router Hardening Guide
doc_type: howto
---

# Router Hardening Guide

Use this baseline before production rollout and after major changes. Adapt network and retention controls to your deployment. Run the self-test from a protected admin workstation:

```sh
metrum-ai-routerctl doctor --security --config config.yaml --format json --fail-on fail
```

Review warnings before using `--fail-on warn` in CI. `--target` probes unauthenticated metrics access with a bounded timeout; use HTTPS for non-loopback targets.

## Admin listener and TLS

Keep `admin.enabled: false` unless the deployment needs the separate control plane. When enabled, bind `admin.listen` to loopback or a private interface. Use `admin.tls.mode: router` with a certificate, or `proxy` behind a trusted TLS terminator. Keep `admin.allow_plaintext_non_loopback: false`. Use `admin.tls.client_auth: require` when admin clients support mTLS. Keep `admin.legacy_routes_on_serving_port: false` after clients move to `/admin/v1`.

Protect the serving listener with `server.tls` when traffic crosses an untrusted network. Set the minimum TLS version to `1.2` or `1.3`. Do not treat a private network as a substitute for authentication.

## Caller and provider keys

Store caller tokens as `callers[].token_hash` with a unique public `token_id`; do not add raw bearer tokens to config. Before upgrading a v4.8 deployment, migrate active caller tokens with the v5 routerctl and remove or clean disabled legacy caller entries. Bind each caller to the smallest `allow` list and set at least one rate or quota limit. Use `status` to disable, suspend, expire, or rotate access.

Keep provider keys in the deployment secret store and set `providers.<name>.api_key_env` to the environment variable name. Do not store provider key values in tracked config, logs, reports, or tickets. Scope provider-side keys to the required models and permissions.

## Spend limits

Set `providers.<name>.spend_breaker.hourly_usd` and/or `daily_usd` where a provider spend ceiling is required. Choose `on_trip: stop` to reject requests or `failover` to try another eligible provider. Breaker counters are per process and reset on restart; multiple replicas can spend up to their combined limits. Use provider-side billing limits for an account-wide cap.

## Sensitive data and tools

Configure `models.<group>.pii_filter` for sensitive groups. Use reviewed expressions, a replacement cap, and `fail_on_match` when partial processing is unacceptable. Regex rules are not complete PII detection; validate them with representative synthetic inputs.

The Router does not execute client tools. Limit the tools exposed by each client and tool host. For `strategy: tool_aware`, `tool_routing.mode: enforce` validates returned calls against offered names and supported argument-schema checks with bounded retry/escalation. It does not authorize tool side effects; the tool host must enforce those permissions.

## Security events and logs

Enable `server.admin_reports.security.enabled` when operators need stored security access reports, with authorized `admin:security_reports` access and a suitable retention policy. When `server.security.events.enabled` is true, choose a protected file or a TLS webhook with a secret supplied through `server.security.events.webhook_secret_env`. Treat downstream event storage as sensitive operational data.

Set `server.logging.path` to protected storage. The Router does not rotate request logs; configure rotation and retention in the deployment's log collector. Logs, events, metrics, and usage rows must not contain raw bearer tokens, provider keys, token hashes, prompts, tool outputs, or full configuration. Do not enable content capture without encryption, access controls, and an approved retention policy.

## Verification

Run `metrum-ai-routerctl doctor --security` against each deployment config and use `--target` from a separate host to check public metrics exposure. It checks listener TLS posture, configured provider key sources and spend ceilings, admin listener settings, security-report enablement, caller limits and verifier format, development/test caller key environments, caller key-age visibility, and sensitive-group DLP. A missing provider spend ceiling is advisory because ceilings are optional. Raw key entropy is not inspected because config stores verifiers only. Review each warning and remediation.

The self-test cannot inspect certificate validity, provider-side permissions, file and database ACLs, client tool-host authorization, event-sink access, or actual log contents. Verify those controls in the deployment and test TLS, authorization, key rotation, DLP blocking, tool restrictions, event delivery, log redaction, and replica behavior in staging.

See the [Threat Summary](./threat-model-v5) and [Compromise Runbook](./compromise-runbook).
