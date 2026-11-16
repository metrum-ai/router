---
title: Honeytokens and canary groups
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Honeytokens and canary groups

Honeytokens are decoy caller keys for detecting accidental or unauthorized
credential use. A honeytoken caller has no administrative permissions. Every
successfully authenticated request using one emits a critical
`security_honeytoken_used` event before any upstream dispatch. The event and
detector signal contain request and caller metadata only: they never contain
the raw token, prompt text, or user message. The router stores only the normal
token verifier; the raw token is written once to a new mode-0600 file by
`metrum-ai-routerctl`.

To bound event writes during repeated use, each honeytoken and canary token
emits at most one event of each event type per minute per router process.
Suppressed repeats increment the Prometheus counter
`metrum_ai_router_honeytoken_events_deduplicated_total`. This bound is
process-local; its LRU tracks at most 10,000 token/event-type pairs, and usage
event retention provides the durable storage bound.

Honeytoken callers may allow only targetless canary groups; config validation
rejects an allow entry for a real model group. For a known, allowed canary
group, completion requests receive a fixed canned response without contacting
a provider. Unknown and disallowed models and unsupported request features
return the same errors as they do for an ordinary caller.

Issue a honeytoken and write it to a protected file:

```sh
metrum-ai-routerctl honeytoken issue \
  --label 'retired deployment manifest' \
  --owner-user security --project decoys --env prod \
  --token-out /secure/path/decoy-token
```

The command prints the caller record, including its verifier, but never prints
the raw token. For database-backed deployments, pass `--router-url` and
`--admin-key-file`; issuance uses `/admin/v1/callers/issue` and commits the
metadata to the active config set before writing the token file. The current
database config projection does not model canary groups, so database-mode
issuance creates a detector-only key with no model allow list.

To add a honeytoken caller to a file config and authorize canary groups in one
operation, pass `--write --config router.yaml --canary-groups canary-a`. The
named groups must already be configured as canaries. A config write creates a
backup; restart or activate the updated configuration before planting the key.
The token file is written only after the config merge succeeds. Keep the token
file restricted and plant the token only in locations approved for the
detection exercise.

A canary model group is a targetless `static` group with an explicit caller
allowlist. It appears only in those callers' model catalogs, including the
Codex catalog. Requests receive a canned completion, emit a critical
`security_canary_group_used` event, and never reach a provider. When the OCSF
security event stream is enabled, honeytoken and canary use events are also
written there as Detection Findings. Configure each selected caller's `allow`
list to include the group and list that caller under
`models.<name>.canary.callers`.

Use `metrum-ai-routerctl honeytoken report --config router.yaml` to list each
planted caller's label, planted time, and most recent observed use. The report
never includes the raw token or verifier. Security events are retained under
the configured usage database retention policy.
