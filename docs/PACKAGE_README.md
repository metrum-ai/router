# Package Bootstrap

This package contains a packaged Metrum AI Router runtime plus a small offline documentation set. The full external administrator documentation is embedded in the router binary and is available at `/docs/` after the service starts.

For product information, visit the [Metrum AI Router website](https://www.metrum.ai/router).

## What Is Included

Binary packages include:

- `bin/metrum-ai-router`
- `bin/metrum-ai-router-token-gen`
- `bin/metrum-ai-router-usage-report`
- `bin/metrum-ai-router-migrate`
- `bin/metrum-ai-routerctl`
- `bin/engine-advisories.json` in release-built packages; `routerctl doctor --security` reads this bounded advisory snapshot by default when present.
- `config/config.example.yaml`
- `config/config.dev.example.yaml`
- `config/env.example.json`
- `caddy/Caddyfile`
- `LICENSE`
- `NOTICE`
- `THIRD_PARTY_NOTICES.md`
- `MODEL_LICENSES.md`
- `docs/`

No prebuilt container image is published. Docker and Compose installs are unsupported; use the binary package.

Choose `linux-amd64` for x86_64 hosts and `linux-arm64` for ARM64 hosts.

## Start Here

Use the quick-start document that matches the package:

- `BINARY_INSTALL.md` for Linux binary packages.
- `KUBERNETES_INSTALL.md` for Kubernetes deployment planning.
- `PACKAGE_VALIDATION.md` for package-content and runtime-health checks.
- `LICENSE.md` for the Apache License 2.0 terms covering all first-party
  content. No separate EULA applies.

At the package root, retain `LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`, and
`MODEL_LICENSES.md` together. `LICENSE` governs first-party content;
`NOTICE` carries distributed notices; `THIRD_PARTY_NOTICES.md` records the
dependency and asset terms and distribution surfaces; and `MODEL_LICENSES.md`
records the boundaries for separately obtained models and datasets. The
inventory files do not turn unresolved terms into permissions, so review them
for the exact artifact and intended redistribution.

After the router is reachable, open:

```text
https://router.example.com/docs/
```

The embedded docs include the complete install, configuration, provider key, caller token, reporting, troubleshooting, upgrade, rollback, and security guidance.

## Required Deployment Inputs

Prepare these deployment-owned files before first startup:

- `config.yaml` copied from `config/config.example.yaml` and reviewed for the deployment.
- `env.json` copied from `config/env.example.json` or equivalent environment variables from a secret manager.
- A durable state directory for router state, logs, and usage data.
- At least one caller token generated with `metrum-ai-router-token-gen`.

Leftover `license.json` files are unused in 3.0.0+ and are not a deployment
prerequisite. Do not store provider keys, raw router tokens, token hashes, full
production configs, or private host details in tickets, public docs,
screenshots, or package notes.

For non-sensitive package questions, use the repository's public question issue
form. Report vulnerabilities through the private path in `SECURITY.md`.
