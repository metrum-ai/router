<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Preparing the release AI review

The release review is generated locally from the exact code commit being reviewed. Use the Codex provider with the selected model and no Anthropic key:

```sh
BASE_TAG=v4.8.0
BASE_COMMIT="$(git rev-parse --verify "${BASE_TAG}^{commit}")"
REVIEW_HEAD="$(git rev-parse --verify HEAD)"
mkdir -p /workspace/v5-release/tmp
OUTPUT_DIR="$(mktemp -d /workspace/v5-release/tmp/release-review.XXXXXX)"

python3 scripts/release_ai_review.py \
  --provider codex \
  --model gpt-6.1-sol \
  --base "${BASE_TAG}" \
  --base-commit "${BASE_COMMIT}" \
  --head "${REVIEW_HEAD}" \
  --tag v5.0.0 \
  --responses docs/release-review-responses.json \
  --artifact-output "${OUTPUT_DIR}/review.json" \
  --output "${OUTPUT_DIR}/AI-release-review.md"

KEY="$(python3 scripts/release_review_artifact_key.py \
  --base "${BASE_TAG}" --base-commit "${BASE_COMMIT}" --head "${REVIEW_HEAD}")"
mkdir -p docs/release-reviews
cp "${OUTPUT_DIR}/review.json" "docs/release-reviews/${KEY}.json"
```

Keep the generated `AI-release-review.md` beside the JSON output for maintainer review; it is not copied into the artifact commit.

The JSON file records the base tag and its commit, the full reviewed commit ID, the provider, model and Codex CLI version, a verdict, the digest, findings and coverage for every changed file. The filename key binds the tag name, base commit and reviewed head. Binary entries contain before/after SHA-256 values. Generated entries identify an allowlisted CI job, which release validation confirms passed on the reviewed head.

If the review reports a blocking finding, the maintainer may record an acceptance reason in `docs/release-review-responses.json`, keyed by the artifact digest. Regenerate the report using that responses file; release validation requires every blocking finding to have a non-empty reason. The artifact's `verdict` records the model's findings, while the report's decision reflects the recorded maintainer response.

Create the artifact change from the exact reviewed head. The artifact commit must be its direct child and contain the generated `docs/release-reviews/${KEY}.json`; it may also include the responses-file update when recording an acceptance. Do not include source changes after the reviewed head. The release validator rejects extra changes between the reviewed head and the release commit.

A maintainer creates the protected `v*` release tag using repository-admin credentials. The release workflow is triggered by an existing tag, verifies that its commit is an ancestor of `main`, and runs `gh release create --verify-tag`; its workflow token cannot create or move a release tag. The workflow validates the committed artifact and does not call a model API.
