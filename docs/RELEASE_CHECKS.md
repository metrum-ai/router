<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Release checks

Every release candidate must pass the checks below. Attach their result files to the release record.

- Run tests for every change included in the candidate.
- Run the package upgrade-and-rollback check. It installs the prior production release as an unprivileged service user, strictly parses migration JSON, smoke-tests serving, restores the prior package and database snapshot, and records elapsed upgrade-and-rollback time.
- Run `go test ./internal/router -run '^TestTenantCacheIsolationEndToEnd$' -count=1`. This two-tenant check uses identical prompts and includes requests without a caller identity.
- Populate the packaged `config/config.example.yaml` with fixture credentials and their actual issuance metadata. Run `metrum-ai-routerctl doctor --security --config <populated-example.yaml> --format json --fail-on warn` and require exit 0. The separately labeled `config/config.dev.example.yaml` is for development only.
- Run the tool-call matrix with `cell_set=release` from the main-branch workflow dispatch and attach the report to the release record.
- Confirm the previous release's performance gate configuration is unchanged.

The upgrade-and-rollback check runs when migration code or migration CLI output changes, and for every release candidate. Its report records versions, commit, check outcomes, and measured upgrade and rollback downtime. It contains no database contents or command output.
