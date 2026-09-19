---
title: License
doc_type: reference
---

# License

All Metrum AI Router first-party content is licensed under Apache-2.0. The
`license.json` described here is an operator runtime-policy input, not a
copyright license or commercial-use condition, and it does not limit
Apache-2.0 rights.

License configuration controls offline signed JSON runtime-policy enforcement.
Operators generate and retain their own Ed25519 signing key, issue
`license.json`, configure the paired public key, and periodically recheck the
file. Normal release builds fail closed when the file is invalid outside any
configured grace period.

Runtime licensing was removed in 3.0.0. `config.example.yaml` no longer contains
`server.license`. A leftover `server.license` block in a deployed config is
accepted and ignored, with one startup warning, through 3.0.0.

Operator-generated verification keys use `server.license.public_keys` as in
[Self-Managed Licensing](../licensing/) and `config.minimal.example.yaml`. The
catalog sample may omit `public_keys` and fall back to embedded runtime keys.

## Schema

`server.license.path` points at the signed runtime license. `state_path` stores safe local license state. The instance-fingerprint fields are mutually exclusive inputs for deployments with instance-bound licenses. Leave revocation disabled unless the operator maintains a signed revocation bundle with the same trust boundary.

When enforcement blocks serving, `/readyz` fails and caller APIs return documented `license-*` errors. Logs, metrics, reports, and admin status APIs may expose only safe scalar license metadata such as status, license ID, customer ID, SKU, key ID, expiry, and grace flag.

## Rollback

Runtime YAML cannot disable licensing in normal packaged deployments. To recover from an invalid renewal, restore the previous signed license file or revocation bundle, restart or wait for recheck, and smoke `/readyz` plus one authenticated caller request.

## Related

See [License-Protected Deployments](../operations/license-protected-deployments), [Self-Managed Licensing](../licensing/), [Local Quickstart](../getting-started/local-quickstart), and [Router Configuration](./router-config).
