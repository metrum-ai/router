<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Mock SDK compatibility

`make compat-mock` builds the two pinned official-SDK testers and their mock
servers from source, caching the resulting binaries under `~/.cache/router-compat`
(or `COMPAT_CACHE`). It builds the working-tree router with `dev_no_license`,
starts all three processes on loopback, runs the explicit 31 OpenAI and 28
Anthropic router-relevant suites from issue #536,
and tears down the processes. No upstream credentials or paid calls are used.
`make ci` runs that target concurrently with Go, Python and Windows checks.
In Actions, docs-only changes skip Go test/vet and Windows lanes; the mock
compatibility tier still builds and exercises the router. Example tests run
only when example or CI-control paths change. Manual runs default to all lanes.

The files under `tests/compat_mock/*-expected.json` map failed suite names to a
reason and a GitHub issue URL. Any new failure, expected failure that passes,
missing suite, bad report or inconsistent tester exit code fails the check.
After a fix, remove the now-passing entry; when adding an expected failure,
link the specific issue and explain the failure. Never update the list merely
to make CI green. To generate a starting baseline deliberately on main, run
`python3 scripts/compat_mock.py --record-main`, then review and refine every
entry against the tester output and tracked issues before committing it. This
option must not be used by CI.
