# Package Validation Bootstrap

Release packages are intentionally small and package-safe. The full administrator documentation is embedded in the router binary and served at `/docs/` after startup.

## Validate Package Layout

Expected binary package layout:

```text
metrum-ai-router-<version>-linux-<arch>/
  bin/
  config/
  caddy/
  docs/
```

Expected Docker Compose package layout:

```text
metrum-ai-router-<version>-docker-linux-<arch>/
  compose/
  config/
  images/
  docs/
```

Package docs are copied only from `scripts/package_docs_allowlist.txt` during
release creation. In a delivered package, the offline docs should include the
bootstrap set, the package-safe solution brief, and `LICENSE.md`. The license
document covers all first-party content under Apache-2.0;
the package does not require EULA acceptance.

The artifact root must also contain `LICENSE`, `NOTICE`,
`THIRD_PARTY_NOTICES.md`, and `MODEL_LICENSES.md`. Treat them as one review set:
the Apache license covers first-party content, while the notice and inventory
files preserve the distinct obligations and boundaries for shipped
third-party components, assets, models, and datasets. Confirm the files match
the exact artifact being reviewed; an inventory entry is not by itself a grant
of rights or evidence that an unresolved term has been cleared.

Do not confuse these files with a leftover runtime `license.json`. That file is
inert in 3.0.0+ (never read) and must not be added to a release package or
treated as software license text.

For a Docker package, load the saved image and version-check all operational binaries, including the non-serving migration runner:

```bash
docker run --rm --entrypoint /app/bin/metrum-ai-router metrum-ai-router:<version>-linux-<arch> --version
docker run --rm --entrypoint /app/bin/metrum-ai-router-token-gen metrum-ai-router:<version>-linux-<arch> --version
docker run --rm --entrypoint /app/bin/metrum-ai-router-usage-report metrum-ai-router:<version>-linux-<arch> --version
docker run --rm --entrypoint /app/bin/metrum-ai-router-migrate metrum-ai-router:<version>-linux-<arch> --version
```

Before a `deployment-job` serving startup, use the canonical `DATA_MIGRATIONS.md` procedure: `plan`, approved backup, `apply`, all release-defined data jobs until each safe state is `validated`, `verify-serving`, then final read-only `status`. `verify-serving` runs schema postconditions and fails unless the ledger is current/compatible and every bound data job is validated. New generic packages use `--driver=sqlite --db=/app/state/usage.sqlite`; PostgreSQL is an explicit deployment substitution using `--driver=postgres --dsn-env=ROUTER_USAGE_DB_DSN`, never a literal DSN. A successful ordinal `0` is not completion evidence.

Packages must not contain:

- private production runbooks;
- private hostnames, IP addresses, SSH usernames, or key paths;
- raw router tokens, token hashes, provider keys, GitHub tokens, or signing material;
- leftover `license.json` or other local state (unused in 3.0.0+), usage databases, logs, or JSONL state;
- source checkout directories such as `docs-site/`, `internal/`, `cmd/`, or `.git`;
- Go source files (`.go`), `go.mod`, or `go.sum`;
- full production config files.

Packaged CLIs (`metrum-ai-router`, `metrum-ai-routerctl`, and related
runtime tools) are prebuilt ELF binaries only. Operator and customer hosts
must not require a Go toolchain or product source tree to run them. Leftover
`license.json` files must not appear
in customer runtime Docker images.

## Runtime Health Checks

After starting the router:

```bash
export ROUTER_BASE_URL="https://router.example.com"
export ROUTER_TOKEN="replace-with-router-token"

curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/version"
curl -fsS "$ROUTER_BASE_URL/docs/"
curl -fsS -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

`/readyz` confirms required runtime checks and configured dependencies. `/docs/` confirms the embedded Docusaurus admin docs are reachable. `/v1/models` confirms the caller token is valid and shows the deployment-defined model groups available to that caller.

For non-sensitive package questions, use the repository's public question issue
form. Report vulnerabilities through the private path in `SECURITY.md`.
