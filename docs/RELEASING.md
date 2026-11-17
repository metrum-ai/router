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

The `release-dry-run.yml` `workflow_dispatch` dry run is limited to `main`. It builds release
packages, compares a clean rebuild, exercises the upgrade/rollback path from
the latest published non-prerelease release, reviews that production range,
and assembles and smoke-tests the multi-architecture image in a local OCI
layout. It does not publish artifacts, push images, or enter the protected
`release` environment. Tag pushes run the same validation and publish only
after approval in that environment.

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

## Live tool-call compatibility evidence (#769)

Dispatch `.github/workflows/agent-tool-compat-live.yml` from `main` with
`cell_set=release` before release checks. The run uses the protected
`evaluation` environment, retains its $1 per-run cap and monthly ceiling, and
attaches the sanitized per-cell report to the Actions run. Both the OpenAI and
Anthropic evaluation keys are required for nightly and release runs; earlier
nightly runs needed only the OpenAI key. The report includes live tool-call
continuations from pinned OpenAI Chat, OpenAI Responses, and Anthropic Messages
Python SDK clients. A failed cell remains visible and needs triage; it is not
converted to a pass by the release selector.

## Release provenance (#332)

For tag builds, the workflow generates GitHub build-provenance attestations
for every published archive, `SHA256SUMS`, and `release-artifacts.json` before
creating the release. The action uses the workflow identity (OIDC) and the
GitHub token; no separate signing secrets are required. Verify each downloaded
asset and the checksum file with `gh attestation verify <file> -R metrum-ai/router`,
then compare the archive's SHA-256 with the matching entry in `SHA256SUMS`.
The `attestation` command set needs GitHub CLI 2.49.0 or newer; older releases
fail with `unknown command "attestation" for "gh"`.
Attestations apply to releases built after this workflow is deployed; they do
not retroactively cover earlier releases.

## Verifiable v5.0.0 release

The tag workflow builds four binary packages from the tagged commit and fixed
source timestamp. A clean runner rebuilds them and publication is blocked
unless their checksums match. On the protected release job, Crane assembles
linux/amd64 and linux/arm64 OCI images from those exact package binaries on a
digest-pinned Chainguard base, then creates a multi-architecture index in
`ghcr.io/metrum-ai/router`. The Dockerfile remains available for local builds.
The job verifies the image binaries against the release packages, runs the
amd64 image binary's version and `/healthz` smoke checks without a container
daemon, signs the images keylessly, and attaches GitHub provenance and SPDX
SBOMs. All six archive and image SPDX JSON SBOMs receive keyless Cosign
signature bundles. The four binary packages each carry an SPDX JSON SBOM,
signature bundle and GitHub build provenance. The AI review report, its
digest-bound findings artifact and `release-images.json` digest manifest are
attached to the release and attested.
The upgrade check and AI review select the preceding stable tag by numeric
semantic version order; release publication time does not affect the baseline.
Each binary archive also includes `bin/engine-advisories.json`, the bounded
snapshot used by `routerctl doctor --security` when no explicit snapshot path
is supplied. The package-content validator requires and validates this file
for release packages; the release dry run generates the same snapshot and
applies the same required-file validation before it can pass.

Publishing, registry writes, signing, attestations, SBOM attachment and GitHub
release creation run only through the protected `release` environment. That
environment must require a reviewer and allow only `v*` tags; the workflow
checks both conditions before publishing. A retry resumes already-pushed
version tags only when their digests match the rebuilt packages; a mismatch
fails closed and requires maintainer investigation before another publish.
The tag job validates a committed AI review artifact for the exact previous stable tag, base commit, release commit, and tag. It does not call a model. Prepare the artifact locally from the exact release commit, record any accepted blocking findings against its digest, and commit the artifact as a direct child of the reviewed commit. The release validator rejects a mismatched range, missing artifact, unanswered blocking finding, or invalid required CI evidence. See [Preparing the release AI review](RELEASE_AI_REVIEW.md) for artifact generation and acceptance steps. A source change after review requires a new artifact for the new commit range.

Verify one downloaded package asset and its signature and provenance:

```sh
cosign verify-blob \
  --bundle metrum-ai-router-v5.0.0-linux-amd64.tar.gz.sigstore.json \
  --certificate-identity 'https://github.com/metrum-ai/router/.github/workflows/release.yml@refs/tags/v5.0.0' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  metrum-ai-router-v5.0.0-linux-amd64.tar.gz
gh attestation verify metrum-ai-router-v5.0.0-linux-amd64.tar.gz -R metrum-ai/router
```

To compare an installed router executable with the signed package for the
current platform, install `cosign` and GitHub CLI, then run:

```sh
metrum-ai-routerctl verify --release v5.0.0 --binary /usr/local/bin/metrum-ai-router
```

If `--binary` is omitted, the command looks for `metrum-ai-router` beside
`metrum-ai-routerctl`, then on `PATH`.

The command verifies the release archive's Cosign bundle and GitHub provenance,
then compares the archive's router binary to the selected local executable.
It fails closed if an asset, signature, provenance record, or matching binary
is missing or differs.

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
