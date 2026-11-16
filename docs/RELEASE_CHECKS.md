<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Release checks

Every release candidate must pass the checks below. Attach their result files to the release record.

- Run tests for every change included in the candidate.
- Run the package upgrade-and-rollback check. It installs the prior production release as an unprivileged service user, strictly parses migration JSON, smoke-tests serving, restores the prior package and database snapshot, and records elapsed upgrade-and-rollback time.
- Run the two-tenant isolation check with identical prompts, including requests without a caller identity.
- Run `routerctl doctor --security --format json --fail-on warn` against the reference configurations.
- Confirm the previous release's performance gate configuration is unchanged.

The upgrade-and-rollback check runs when migration code or migration CLI output changes, and for every release candidate. Its report records versions, commit, check outcomes, and measured upgrade and rollback downtime. It contains no database contents or command output.
