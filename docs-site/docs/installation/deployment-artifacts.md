---
title: Deployment Artifacts
doc_type: howto
---

# Deployment Artifacts

Metrum AI Router is delivered as release artifacts. Deployment hosts do not need language runtimes, documentation tooling, compilers, or package-build tooling.

## Artifact Types

| Artifact | Use when | Contents |
|---|---|---|
| Linux binary package (`.tar.gz`) | The platform team manages the service supervisor, TLS proxy, database, and filesystem layout on Linux. | Router binaries, config templates, Caddy example, package-safe bootstrap docs. |
| Windows binary package (`.zip`) | The platform team manages process supervision, TLS proxy, database, and filesystem layout on Windows. | Same package layout as Linux, with Windows `.exe` binaries, config templates, Caddy example, package-safe bootstrap docs, and legal files. |
| Docker Compose package | The platform team wants a packaged router image tar plus Compose files with durable single-writer SQLite by default. | Saved image tar, SQLite-first Compose files, an explicit localhost-only PostgreSQL override, config templates, package-safe bootstrap docs. |
| Kubernetes manifests or Helm | The platform team operates Kubernetes and supplies reviewed manifests. | Obtained as a reviewed deployment artifact when needed; see [Deploy To Kubernetes](./kubernetes). |

Use `linux-amd64` / `linux-arm64` for x86_64 / ARM64 Linux hosts, and `windows-amd64` / `windows-arm64` for x86_64 / ARM64 Windows hosts. Windows binary archives are named `metrum-ai-router-<version>-windows-<arch>.zip`; Linux archives remain `metrum-ai-router-<version>-linux-<arch>.tar.gz`. Docker packages remain Linux-only, use the Linux architecture suffix, and include exactly one saved image tar for that architecture.

## Embedded Docs And Admin Assets

The router binary embeds the product documentation site. After startup, browser requests to `/docs/` show the full external administrator docs, including installation, configuration, provider keys, caller tokens, reporting, troubleshooting, upgrade, rollback, and security guidance.

Authenticated admin report assets, when enabled, are embedded separately and served under `/admin/reports/`. They are not part of public `/docs/` and require browser-admin authentication plus authorization.

## Offline Package Docs

Release tarballs include a small package-safe Markdown bootstrap set under `docs/`. These files explain what is in the package, how to start it far enough to reach `/docs/`, validation commands, and support contact paths.

The binary artifact root (including Windows ZIPs) also carries `LICENSE`,
`NOTICE`, `THIRD_PARTY_NOTICES.md`, and `MODEL_LICENSES.md`. Preserve and review
all four when copying or redistributing an artifact. See [Software License And Third-Party Notices](../legal/software-licenses)
for the scope of each notice file. Leftover runtime `license.json` files are
inert in 3.0.0+.

Use the embedded `/docs/` site for the full product guide after the router starts. Keep deployment-specific credentials, host paths, and operational notes in the customer's approved private systems.

## Package Selection Checklist

- Match the artifact architecture to the host CPU.
- Choose a Linux or Windows binary install when the deployment already has a process supervisor, database, and TLS proxy; Windows packages do not install a service, and Caddy is not bundled as a service.
- Windows binaries are not Authenticode-signed currently; apply your organization's executable approval controls.
- Choose Docker Compose when a packaged image tar and Compose-managed Postgres fit the operating model.
- Use Kubernetes only with reviewed deployment manifests and the canonical Kubernetes install guide.
- Confirm `/readyz`, `/docs/`, `/v1/models`, and one caller smoke before sending production traffic.

Next steps:

- [Install From Linux Or Windows Binaries](./binary)
- [Run With Docker Compose](./docker-compose)
- [Deploy To Kubernetes](./kubernetes)
- [Package Validation And Security Checks](./package-validation)
