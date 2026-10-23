---
title: Documentation Privacy
doc_type: reference
---

# Documentation Privacy

This open-source documentation is delivered as static content embedded in the
router release. The documentation pages are separate from router request and
usage records.

Do not submit provider keys, caller tokens, token hashes, credentials, prompts,
raw images, customer data, private hostnames, or full production configuration
through public repository issues. Suspected vulnerabilities use the private
reporting path in the repository `SECURITY.md`.

Router usage retention is deployment-configured under `server.retention`.
Governed content capture is disabled by default and, if enabled, requires its
documented encryption, authorization, and retention controls. The deployment
operator is responsible for publishing any privacy notice required for its own
users, infrastructure, providers, and data-handling choices.

Training export is a separate privileged content operation, not ordinary usage
reporting. `POST /admin/training/export` requires explicit deployment-wide
`content:training` `export` authorization and a durable security audit event;
harness labels require the separate `label` action. Only existing complete,
encrypted, unexpired opt-in captures are eligible. Caller keys and ordinary
report permissions do not grant export. Headers and raw vendor request extensions
are not training fields. Storage redaction is preserved and content PII/secret
filters apply without restoration. Regex filtering is not a universal sensitive-
data guarantee; deployment operators must review consent and policy.

Exported JSONL and manifests are customer-controlled copies. Deleting/expiring
router captures prevents later exports but cannot recall downloaded artifacts;
secure downstream access, retention and deletion independently. Legal holds do
not grant training rights. Verify that the selected teacher's model license and
provider terms permit training on outputs before enabling training export. The
synthetic Acme example's scoped maintainer decision is not permission to train
on customer outputs.

When model-group PII filtering uses response restoration, restoration applies
to buffered responses. Same-dialect native OpenAI Chat, OpenAI Responses, and
Anthropic Messages streams preserve the generated placeholders in caller-visible
SSE. The router does not attempt per-chunk restoration because a placeholder may
span arbitrary upstream chunks; buffering to reconstruct it would remove native
streaming behavior. This is a transport behavior, not a claim about provider
privacy or data handling.

Model-group targets may carry an optional deployment-defined `region` label.
The selected value is ordinary scalar diagnostics metadata and follows the
configured usage-retention policy. It helps operators audit the location they
assigned to the target, including a target reached by fallback. It is not an
independent residency, transfer, retention, jurisdiction, or provider-training
guarantee. Deployment operators remain responsible for verifying provider
location and data-handling terms and for configuring routing policy that meets
their requirements.

