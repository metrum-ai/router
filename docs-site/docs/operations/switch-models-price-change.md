---
title: "Switch models when prices change"
doc_type: howto
---

# Switch models when prices change: swap, shadow, promote, and roll back in one API call

Model prices and lineups change every few weeks. When OpenAI cut GPT-6.1 Terra to roughly half the GPT-5.6 Terra price on 2026-10-26, the value of the cut depended on moving production traffic the same day — safely, and reversibly.

The Admin Config API makes that one authenticated HTTP call per step. Applications keep calling the same model group (`agent-default`); nothing restarts; no YAML is edited.

Prerequisites:

- The router runs in database-backed mode (`server.config_source.mode: database`). See [Admin Config API](../reference/admin-config-api#file-mode).
- You hold an admin API key with `config:read`, `config:write`, and (for shadowing) `shadow:write`. See [Admin Authentication](../configuration/admin-authentication).

The worked example below moves group `agent-default` from `openai/gpt-5.6-terra` to `openai/gpt-6.1-terra`, with the self-hosted fallback pool kept in place.

## 1. Look at the current group

```bash
export ROUTER_URL=https://router.example.com
export ROUTER_ADMIN_KEY_FILE=~/.config/router/ops-primary.key
metrum-ai-routerctl groups show agent-default
```

Note the `revision` — it is your optimistic-concurrency handle.

## 2. Dry-run the swap

```bash
metrum-ai-routerctl groups set-targets agent-default \
  --target openai/gpt-6.1-terra --target self-hosted-vllm/qwen3.8-27b \
  --strategy failover --reason "GPT-6.1 Terra price cut (2026-10-26)" \
  --dry-run
```

The response is the diff (`added`, `removed`, `reordered`) and a per-target eligibility report. `dry_run` never changes the active set. If a target fails catalog or contract checks, you get `422 target-ineligible` with the failing checks named, and nothing changes.

Equivalent raw call:

```http
PUT /admin/config/groups/agent-default/targets
If-Match: "12"
Idempotency-Key: terra-swap-2026-10-27-001

{"targets": [{"provider": "openai", "model": "gpt-6.1-terra"},
             {"provider": "self-hosted-vllm", "model": "qwen3.8-27b"}],
 "strategy": "failover", "reason": "GPT-6.1 Terra price cut", "dry_run": true}
```

## 3. Optional: shadow the candidate first

When you want production evidence before committing, shadow the candidate against real traffic:

```bash
metrum-ai-routerctl groups shadow start agent-default \
  --target openai/gpt-6.1-terra \
  --sample-rate 0.5 --max-requests 500 --cost-cap-usd 5 \
  --reason "shadow Terra 6.1 before cutover"
metrum-ai-routerctl groups shadow status agent-default
```

The status call compares shadow vs served cost, tokens, latency, and error rate over the run window. If it looks wrong, `groups shadow stop agent-default` ends it with no serving change at all.

## 4. Commit the swap

```bash
metrum-ai-routerctl groups set-targets agent-default \
  --target openai/gpt-6.1-terra --target self-hosted-vllm/qwen3.8-27b \
  --strategy failover --reason "GPT-6.1 Terra price cut (2026-10-26)" \
  --if-match 12
```

Or, from a shadow you liked, one call:

```bash
metrum-ai-routerctl groups shadow promote agent-default --reason "shadow passed: 40% cheaper, p95 within SLO"
```

The write is one transaction: import the patched document as a new set, then activate it. The writing replica serves the new target immediately; the others follow within the revision propagation window (default 2s on SQLite, near-immediate on PostgreSQL). In-flight requests, including streams, finish on the old target.

Retry-safety: the CLI sends an `Idempotency-Key` on every mutation, so a retried command returns the recorded result instead of applying twice.

## 5. Verify

```bash
metrum-ai-routerctl groups show agent-default          # new targets, new revision
metrum-ai-routerctl groups history agent-default       # who changed what, when, why
metrum-ai-routerctl config diff --api                  # group diff between the sets
```

`GET /admin/config/status` on each replica shows its loaded revision converging.

## 6. Roll back in one call if the new model misbehaves

```bash
metrum-ai-routerctl groups rollback agent-default --reason "Terra 6.1 elevated error rate"
```

That restores the group's exact previous targets from the recorded history (or pass `--set-id` to pick the set). For whole-document rollback, `metrum-ai-routerctl config rollback --api --set-id set-…` re-activates a prior configuration set.

## What callers see

Nothing. The group name, `/v1/models` entries, and API shapes are unchanged across a swap; in-flight streams complete on the old target; new requests land on the new one within the propagation window.
