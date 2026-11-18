<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Release checks

Every release candidate must pass the checks below. Attach their result files to the release record.

- Run tests for every change included in the candidate.
- Run the package upgrade-and-rollback check. It installs the prior production release as an unprivileged service user, strictly parses migration JSON, smoke-tests serving, restores the prior package and database snapshot, and records elapsed upgrade-and-rollback time.
- Run `go test ./internal/router -run '^TestTenantCacheIsolationEndToEnd$' -count=1`. This two-tenant check uses identical prompts and includes requests without a caller identity.
- Run `go test ./cmd/metrum-ai-routerctl -run "^(TestSecurityDoctorReferenceConfigs|TestSecurityDoctorCommandJSONAndThreshold)$" -count=1` to check the reference templates and CLI exit thresholds. The templates illustrate several deployment profiles and the reference test permits specific findings. For example, the packaged `config/config.example.yaml` includes development callers and leaves provider spend ceilings unset. Run `metrum-ai-routerctl doctor --security --config <populated-config.yaml> --format json --fail-on warn` on the populated example and retain its exit code and JSON findings in the release record. A warning exits 1. Apply the same strict check to the provisioned deployment configuration before rollout.
- Run the tool-call matrix with `cell_set=release` from the main-branch workflow dispatch and attach the report to the release record.
- Confirm the previous release's performance gate configuration is unchanged.

The upgrade-and-rollback check runs when migration code or migration CLI output changes, and for every release candidate. Its report records versions, commit, check outcomes, and measured upgrade and rollback downtime. It contains no database contents or command output.
