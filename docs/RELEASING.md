<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Releasing Metrum AI Router

Releases are immutable and one-step: push the annotated `vX.Y.Z` tag and the
`release.yml` workflow builds packages, validates the inventory, and creates
the GitHub Release with the assets in one step. Do not create an empty
release first.

```sh
git tag -a vX.Y.Z -m "Metrum AI Router vX.Y.Z"
git push origin vX.Y.Z
```

`workflow_dispatch` runs of `release.yml` are build-only dry runs (no release
is created) and never sign Windows binaries.

## Windows Authenticode signing (#332)

Windows `bin/*.exe` files are Authenticode-signed with Azure Trusted Signing
(RFC 3161 timestamped) when the signing secrets are provisioned. The sign
step runs between the `.exe` builds and the deterministic zip packaging, so
`SHA256SUMS` and `release-artifacts.json` always hash the signed binaries.

- The workflow reads the secrets `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`,
  `AZURE_CLIENT_SECRET`, `TRUSTED_SIGNING_ENDPOINT`, `TRUSTED_SIGNING_ACCOUNT`,
  `TRUSTED_SIGNING_CERT_PROFILE`. They live in the central vault and are
  pushed to GitHub Actions secrets from the ops side only; nothing is stored
  in this repository.
- While the secrets are absent, signing is a logged no-op and releases ship
  unsigned Windows binaries (pre-provisioning state).
- **Once the certificate profile is provisioned, set the repository variable
  `WINDOWS_SIGNING_REQUIRED=true`.** Tag builds then fail instead of shipping
  unsigned Windows assets when the secret set is missing or partial
  (`scripts/sign_windows_exes.py` enforces both). Do not set the variable
  before the secrets exist.
- After provisioning, run a `workflow_dispatch` dry run on a test tag and
  verify the signature on a real Windows host:
  `Get-AuthenticodeSignature .\bin\metrum-ai-router.exe` (Status `Valid`,
  subject `Metrum AI`, timestamp countersignature present).
- Rotation: rotate the client secret at least every 6 months from the vault,
  or switch to GitHub OIDC federated credentials and drop the client secret.

Metrum AI
