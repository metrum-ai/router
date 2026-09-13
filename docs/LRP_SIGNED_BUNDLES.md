# LRP operator-signed bundles

Follow-up to issue
[#33](https://github.com/metrum-ai/router/issues/33).
Learned Routing Policy (LRP) v1 already loads immutable bundles with a SHA-256
manifest inventory and path containment checks. Operators may optionally attach
an Ed25519 detached signature so production loaders can require a trusted
signer before accepting a candidate bundle.

This is operator-owned trust material only. Generate and retain your own
keypair. Do not commit private keys, customer licenses, or real production
trust stores. Tests generate ephemeral fixtures in memory and temporary
directories.

## What is signed

Signing covers `manifest.json` bytes and the content-addressed
`manifest.version` digest already computed by the bundle format:

| Field | Role |
|---|---|
| `manifest_sha256` | SHA-256 of the exact `manifest.json` file bytes on disk |
| `manifest_version` | Existing 24-hex content digest of the canonical manifest without the `version` field |
| `key_id` | Operator-chosen trust identifier used for rotation |
| `signature_base64` | Ed25519 signature over a fixed binding message |

The signed message is UTF-8:

```text
lrp.bundle.sig.v1
ed25519
<key_id>
<manifest_version>
<manifest_sha256>
```

Artifact file digests under `manifest.files` remain independently verified by
the existing loader. The signature therefore binds the inventory that already
hash-checks every model and embedding file.

The detached envelope is written next to the manifest as `manifest.sig` with
schema `lrp.bundle.sig.v1`. Unsigned bundles remain loadable when
`require_signed` is false and no signature file is present.

## Trust store and rotation

Trust is a small JSON document (`lrp.bundle.trust.v1`) listing one or more
public keys:

```json
{
  "schema_version": "lrp.bundle.trust.v1",
  "keys": [
    {
      "key_id": "lrp-operator-2026-01",
      "algorithm": "ed25519",
      "public_key_base64": "<32-byte public key, standard base64>"
    },
    {
      "key_id": "lrp-operator-2026-06",
      "algorithm": "ed25519",
      "public_key_base64": "<next public key>"
    }
  ]
}
```

Rotation procedure:

1. Generate a new Ed25519 keypair on a protected operator host.
2. Add the new public key to the trust store while keeping the previous key.
3. Re-sign promoted bundles with the new `key_id`.
4. Deploy the updated trust store to loaders that set `require_signed`.
5. After every active bundle verifies under the new key, remove the retired
   public key from trust to revoke it.

A signature whose `key_id` is absent from the trust store fails closed.

## Operator commands

Private keys are base64 of a 32-byte Ed25519 seed (or a 64-byte seed||public
blob). Keep mode `0600` files outside the repository.

```bash
# After training/validation, detach-sign a bundle directory.
uv run --project services/learned-routing-policy --locked \
  lrp sign-bundle --bundle "$LRP_BUNDLE_DIR" --key "$LRP_SIGNING_KEY" --key-id lrp-operator-2026-01

# Verify the detached signature against an operator trust store.
uv run --project services/learned-routing-policy --locked \
  lrp verify-bundle --bundle "$LRP_BUNDLE_DIR" --trust "$LRP_TRUST_JSON" --require-signed

# Hash-check plus optional require-signed gate.
uv run --project services/learned-routing-policy --locked \
  lrp validate --bundle "$LRP_BUNDLE_DIR" --trust "$LRP_TRUST_JSON" --require-signed

# Serve with the same trust policy on startup and every reload/SIGHUP.
uv run --project services/learned-routing-policy --locked \
  lrp serve --bundle "$LRP_BUNDLE_DIR" --config "$LRP_DATA_DIR/lrp.yaml" \
  --trust "$LRP_TRUST_JSON" --require-signed --enable-admin
```

Python API:

```python
from lrp.bundle import load_bundle, sign_bundle, write_trust_keys

sign_bundle(bundle_dir, private_seed, "lrp-operator-2026-01")
load_bundle(bundle_dir, require_signed=True, trusted_keys=trust_path)
```

If `manifest.sig` is present, loaders must receive a trust store; the signature
is never skipped silently. `lrp serve --trust` / `--require-signed` propagates
the same settings into startup load and every admin/SIGHUP reload. Failed signed
reloads keep the previously loaded bundle and matching ensemble
(`Runtime.reload` / `AtomicBundle.reload`), matching the existing digest-failure
rollback behavior.

## Rollback

- Keep the previous validated bundle directory and its matching `manifest.sig`.
- Point serve/reload at the prior directory after a failed candidate.
- Roll back policy influence independently with router `mode: baseline` or the
  prior external-policy config, as described in the
  [operator runbook](LEARNED_ROUTING_POLICY.md#validation-promotion-and-rollback).
- Retiring a compromised signing key means removing it from trust and
  re-signing any still-needed bundles with a replacement key.

## Negative cases covered by unit tests

- Unsigned bundle rejected when `require_signed=True`
- Signature present without trust store rejected
- Tampered signature bytes rejected
- Broken `manifest_sha256` / version hash binding rejected
- Unknown or retired `key_id` rejected
- Failed signed reload preserves the previous `AtomicBundle` snapshot
- Existing unsigned SHA-256 containment checks still pass without signatures

Synthetic wiring tests use generated keys only. They do not authorize live
routing activation or substitute for provider-backed promotion evidence.
