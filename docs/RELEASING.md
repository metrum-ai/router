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

## Required hierarchy compatibility matrix (#434)

The release workflow packages a tag only after the hierarchy compatibility
matrix is green. `hierarchy-matrix.yml` runs the two local mock matrices that
provide the shipped hierarchy evidence: the agent client chain matrix
(`make agent-chain-mock`, depths 0, 1, 2, 3 and 5 for pi, LangGraph, OpenAI
Agents SDK and Claude Code, plus the Claude Messages to OpenAI Chat cells) and
the sandboxed Codex Responses chain matrix. Both are loopback fixtures with no
hosted model calls, so the step costs nothing and needs no provider keys.

`scripts/hierarchy_release_gate.py` decides the result; the runner exit status
alone is not the gate. Every cell in the published JSON reports must be `pass`,
or be listed in `tests/agent_compat/release-exceptions.json`:

```json
{
  "schema": "router-hierarchy-release-exceptions-v1",
  "exceptions": [
    {
      "cell": "pi/same-dialect/depth-1",
      "reason": "one line on why this cell is accepted as red",
      "issue": "https://github.com/metrum-ai/router/issues/430",
      "approved": "2026-11-02"
    }
  ]
}
```

Rules the gate enforces:

- an exception must name a cell that actually ran, carry a short reason, link a
  `metrum-ai/router` issue, and record the approval date; the exception file is
  part of the reviewed diff, so an exception is only as good as its merge;
- an exception for a cell that passes is **stale** and fails the gate, so the
  file cannot drift into covering cells that no longer need it;
- a missing, empty or malformed report is a failure, never an empty matrix;
- the release workflow runs the gate on the tagged commit, so a result is only
  ever used for the commit it was produced on.

Run the same decision locally after a matrix run:

```sh
python3 scripts/hierarchy_release_gate.py \
  --report tests/agent_compat/.out/chain-matrix.json \
  --report tests/agent_compat/.out/codex-chain-matrix.json
```

Do not widen the exception file, drop matrix cells, or change the runners to
make a release pass. A red cell that cannot be covered by a reviewed exception
blocks the release; report it rather than working around it.

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
