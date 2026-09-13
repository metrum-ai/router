---
title: Operator-signed LRP bundles
doc_type: howto
---

Learned Routing Policy bundles are content-addressed model snapshots. Operators
may optionally attach an Ed25519 detached signature so serving hosts only load
bundles signed by a trusted key.

Callers do not send signatures. The router still presents eligible targets; LRP
loads operator-owned artifacts offline or at process start. Signing is a
deployment control, not a caller API.

## Operator workflow

1. Train and validate a bundle with the usual `lrp train` / `lrp validate` flow.
2. Generate an operator-owned Ed25519 keypair on a protected host. Keep the
   private key out of the repository and out of container images.
3. Write a trust JSON listing one or more public keys (`key_id` plus base64
   public key). Multiple keys support rotation.
4. Run `lrp sign-bundle --bundle <dir> --key <private> --key-id <id>` to write
   `manifest.sig` beside `manifest.json`.
5. Serve and reload with the same trust policy:
   `lrp serve --bundle <dir> --config <yaml> --trust <trust.json> --require-signed`.
   Startup and every admin/SIGHUP reload enforce require-signed and verification.
   A present signature is never skipped silently.

The signature binds the exact `manifest.json` bytes and the existing manifest
version digest. Per-file SHA-256 inventory checks still run. Unsigned bundles
remain valid for local development when no signature file is present and
require-signed loading is off.

See the operator document
[LRP signed bundles](https://github.com/metrum-ai/router/blob/main/docs/LRP_SIGNED_BUNDLES.md)
for trust rotation, rollback, and negative-case behavior. Contact
[contact@metrum.ai](mailto:contact@metrum.ai) for deployment questions.
