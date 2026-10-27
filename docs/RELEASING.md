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

A `workflow_dispatch` run on a non-tag ref is build-only; a dispatch on a
matching tag ref can publish a release. Do not use a real tag as a dry run.

## Release provenance (#332)

For tag builds, the workflow generates GitHub build-provenance attestations
for every published archive, `SHA256SUMS`, and `release-artifacts.json` before
creating the release. The action uses the workflow identity (OIDC) and the
GitHub token; no separate signing secrets are required. Verify each downloaded
asset and the checksum file with `gh attestation verify <file> -R metrum-ai/router`,
then compare the archive's SHA-256 with the matching entry in `SHA256SUMS`.
Attestations apply to releases built after this workflow is deployed; they do
not retroactively cover earlier releases.

## Optional Windows Authenticode signing

The release default does not require Azure or Authenticode. The existing
Trusted Signing step remains dormant when its secrets are absent; Windows
binaries then remain unsigned. Operators may explicitly opt in by provisioning
all six secrets. When enabled, signing runs before ZIP packaging, so the
checksums and inventory hash the signed binaries.

- The workflow reads the secrets `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`,
  `AZURE_CLIENT_SECRET`, `TRUSTED_SIGNING_ENDPOINT`, `TRUSTED_SIGNING_ACCOUNT`,
  `TRUSTED_SIGNING_CERT_PROFILE`. Provision them only if opting in; do not
  store secret values in this repository.
- While the secrets are absent, signing is a logged no-op and releases ship
  unsigned Windows binaries (pre-provisioning state).
- **Once the certificate profile is provisioned, set the repository variable
  `WINDOWS_SIGNING_REQUIRED=true`.** Tag builds then fail instead of shipping
  unsigned Windows assets when the secret set is missing or partial
  (`scripts/sign_windows_exes.py` enforces both). Do not set the variable
  before the secrets exist.
- If opting in, validate the configured signing path in an approved test
  environment and verify the signature on a real Windows host:
  `Get-AuthenticodeSignature .\bin\metrum-ai-router.exe` (Status `Valid`,
  subject `Metrum AI`, timestamp countersignature present).
- If enabled, rotate the client secret according to the credential policy.
  The current signing implementation requires all six secrets.

Metrum AI
