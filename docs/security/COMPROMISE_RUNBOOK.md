<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Router Compromise Runbook

Use this procedure when a provider key, caller key, admin credential, or Router host may be compromised. Work from a trusted admin workstation and deployment channel. Do not paste credentials, prompts, raw request bodies, or full config into tickets or chat.

## 1. Detect and scope

- Record discovery time, affected deployment, public caller `token_id` or provider key label, and safe request/event IDs.
- Review authorized security reports, Router logs, provider-side key activity, and deployment audit records.
- Check for unexpected callers, model groups, egress, admin changes, or configuration revisions.
- Preserve relevant logs and metadata under the incident retention policy. Do not collect raw prompts or secret values as evidence.

## 2. Contain

### Caller key or admin credential

Suspend the caller through the authorized containment operation when available, or disable its caller status through the control plane. Remove admin permissions or revoke the affected admin credential. Confirm the status change and test that the old credential receives a denial. If the operation fails or times out, treat containment as unconfirmed and verify the authoritative key state before proceeding.

### Provider key

Revoke or disable the provider key at the provider first. If the Router is sending requests with it, disable the affected provider or route traffic to a verified alternate. Do not rely on a Router restart to revoke a provider credential.

### Router host

Isolate the host from untrusted inbound and upstream networks while preserving required forensic evidence. Stop the compromised process if it can continue making requests or changing policy. Do not restore service from the suspect host; rebuild from a trusted image and reviewed configuration.

## 3. Rotate credentials

1. Create replacement credentials from a trusted system.
2. Update the deployment secret store, not tracked config or logs.
3. Update the Router's environment reference or caller verifier through the approved control plane.
4. Reload or restart using the deployment procedure.
5. Verify new credentials work and old credentials fail. Check all replicas and both serving and admin paths.
6. For a host compromise, also rotate deployment credentials, admin sessions, webhook secrets, TLS private keys when exposed, and any provider or caller key that was accessible to the host.

Rotate provider keys before reopening affected traffic. Rotate caller keys before re-enabling the caller. A compromised host requires a clean rebuild before any service resumes.

## 4. Review and recover

- Review provider billing and access activity, admin audit records, security events, and safe request evidence for the exposure window.
- Check for unauthorized configuration, policy, certificate, detector, or webhook changes.
- Restore only reviewed config and required routes. Re-enable a suspended caller only after the owner confirms rotation and access scope.
- Monitor for renewed denials, key use, unusual spend, and unexpected egress.
- Record the timeline, affected identifiers, containment result, rotations completed, evidence references, and follow-up actions.

If containment or rotation cannot be verified, keep the affected credential or route disabled and escalate through the deployment incident process.

See the [Hardening Guide](./hardening) and [Threat Summary](./threat-model-v5).
