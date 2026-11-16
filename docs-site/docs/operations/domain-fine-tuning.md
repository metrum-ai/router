<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governed domain fine-tuning workflow

Use this workflow only when the domain owner has approved training use, the
teacher model license and provider terms allow output training, and an operator
can secure both the router and every exported copy. The router captures only
when capture is explicitly enabled for the selected caller, model group, or
server. Keep separate caller keys and projects for each data domain. An enabled
capture policy is the deployment's opt-in boundary; do not route unrelated
traffic through an opted-in key.

## Capture and export

Enable encrypted capture for the chosen domain with a short retention period.
Configure request and response capture only as needed, set redaction before
storage, and provide deployment-managed encryption key custody. Capture stays
disabled for all other keys and domains. Send real, authorized traffic through
the router and attach outcome labels using the approved evaluation harness.
Labels may reference only complete, encrypted, unexpired captures; callers
cannot upload transcripts or label their own requests.

Use `POST /admin/training/export` with an explicit domain, half-open UTC time
window, frozen validation and test split boundaries, teacher terms decision,
and task source license. Export is bounded and audited before content leaves the
router. Inspect the manifest and split counts before training. Exported data is
not rewritten by later deletion or expiry, so store it in an access-controlled
directory and set an independent deletion date.

The checked-in [router-loop example](../../../examples/fine-tune-qwen3p8-27b/router-loop/README.md)
demonstrates local collection, authorized labels, export, and replay fixtures.
It uses an external trainer step: unpack the export in a controlled training
environment, train and serve the candidate there, and keep trainer credentials
and artifacts outside the router. No hosted Metrum service is required. The
example's synthetic candidate proves caller-scoped routing only; it is not a
trained-model result.

## Gate, shadow, promote, and roll back

Before training, freeze the task cohort, scorer, preregistered quality and
regression thresholds, and maximum shadow request, duration, and cost budgets.
Run the external trainer, evaluate the candidate against the frozen validation
and test sets, and reject it if any gate fails. Register the passing candidate
in the router catalog with its validation evidence before shadowing it.

Use the same model group for a bounded shadow run. For example:

```sh
metrum-ai-routerctl groups shadow start support-agent \
  --target ft-canary/candidate-v1 --sample-rate 0.05 --max-requests 100 \
  --duration-seconds 3600 --cost-cap-usd 2 --reason "preregistered gate run"
metrum-ai-routerctl groups shadow status support-agent
```

Review aggregate shadow errors, latency, token use, and cost against the frozen
gate. Stop the shadow on any failure. Promote only after the candidate meets
every gate:

```sh
metrum-ai-routerctl groups shadow promote support-agent \
  --reason "candidate passed frozen validation and shadow gates"
```

Promotion is audited and retains the previous group set. If production
regressions appear, restore the prior group set in one call:

```sh
metrum-ai-routerctl groups rollback support-agent \
  --reason "rollback after production regression"
```

Admin API writes require the deployment's config and shadow permissions. The
router propagates config revisions to replicas through its shared control
plane. Keep provider and admin credentials outside command history; use the
CLI's protected key file or environment configuration. Shadowing and promotion
do not prove model quality by themselves.

## Retention and deletion

Review retention status before deleting eligible captures. Purge expired rows
through the authorized maintenance API or configured retention job; delete
individual captures when consent is withdrawn. Verify that the capture and its
training labels are gone and that a later export excludes them. Deletion cannot
recall datasets or model artifacts already copied to the training environment;
apply the same retention and deletion policy there. Legal holds preserve data
but do not grant permission to train on it.

Do not fine-tune when the domain owner has not approved the purpose, provider
terms or license prohibit training, the eligible sample is too small or
unrepresentative, a retrieval or prompt change is sufficient, or the cost and
privacy risk exceed the expected value. The example's synthetic routing proof
does not establish quality improvement, production latency, or return on
investment.
