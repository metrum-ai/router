---
title: Honeytokens and canary groups
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Honeytokens and canary groups

Honeytokens are decoy caller keys for detecting accidental or unauthorized
credential use. A honeytoken caller has no administrative permissions. Any
valid completion request using it receives a fixed canned response, emits a
critical `security_honeytoken_used` event, and never reaches a provider. The
router stores only the normal token verifier; the raw token is written once to
a new mode-0600 file by `metrum-ai-routerctl`.

Issue a honeytoken and write it to a protected file:

```sh
metrum-ai-routerctl honeytoken issue \
  --label 'retired deployment manifest' \
  --owner-user security --project decoys --env prod \
  --token-out /secure/path/decoy-token
```

To add its caller record to a config and authorize canary groups in the same
operation, pass `--write --config router.yaml --canary-groups canary-a`. The
named groups must already be configured as canaries. A config write creates a
backup; restart or activate the updated configuration before planting the key.
Keep the token file restricted and plant the token only in locations approved
for the detection exercise.

A canary model group is a targetless `static` group with an explicit caller
allowlist. It appears only in those callers' model catalogs. Requests receive a
canned completion, emit a critical `security_canary_group_used` event, and
never reach a provider. Configure each selected caller's `allow` list to include
the group and list that caller under `models.<name>.canary.callers`.

Use `metrum-ai-routerctl honeytoken report --config router.yaml` to list each
planted caller's label, planted time, and most recent observed use. The report
never includes the raw token or verifier. Security events are retained under
the configured usage database retention policy.
