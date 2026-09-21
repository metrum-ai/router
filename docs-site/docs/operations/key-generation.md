---
title: User Key Generation
doc_type: howto
---

# User Key Generation

Router caller tokens authenticate applications, users, or evaluation jobs to Metrum AI Router. Tokens use a readable prefix for traceability plus a random secret suffix. The router stores only token hashes.

`router-token-gen` is an administrative CLI shipped in the release package for platform administrators. Run it from a secure server console, deployment host shell, or controlled administrator workstation, and distribute only the generated router tokens to approved callers.

## Generate A Token

```bash
router-token-gen generate \
  --owner-user example-user \
  --project example-project \
  --env prod \
  --allow <allowed-model-group>[,<allowed-model-group>...]
```

The tool prints:

- The full token to give to the caller once.
- A public `token_id` used in logs and reports.
- A `callers:` YAML entry containing `owner_user`, `project`, and `token_sha256`.

Before the caller token can authenticate, `owner_user` must exist in `users`, `project` must exist in `projects`, and `project_memberships` must contain an active membership for that user/project pair. Each configured user id, project id, caller `id`, `token_sha256`, and non-empty `token_id` must be unique after normalization. Token hashes are checked case-insensitively, and duplicate-hash validation errors identify the caller IDs without printing hash values. User, project, and membership statuses support `active`, `disabled`, `suspended`, `removed`, and `archived`; caller token statuses also support `expired` and `rotated`. `--username` is an alias for `--owner-user`; `--user` remains a deprecated compatibility alias.

## Identity Model

The router uses explicit account records:

- `users` identify people, services, administrators, or evaluation jobs.
- `projects` identify business units, applications, cost centers, environments, or evaluation scopes.
- `project_memberships` grant a user a project role such as developer, operator, owner, auditor, or another deployment-defined role.
- `callers` are caller-token config entries that reference one owner user, one project, one environment, and one allow list.

Do not infer ownership or authorization from the token prefix alone. The prefix and public `token_id` are traceability aids; authorization comes from the caller token's configured owner/project references, membership status, allow list, quotas, and admin authorization policies where applicable. One user/project can have multiple caller tokens for rotation, production versus staging, separate clients, higher-TPM coding-agent traffic, or short-lived evaluations.

## Access Patterns

```yaml
allow: [example-general, example-low-cost]
```

Use this pattern for users or applications that should only see a smaller set of deployment-defined groups.

```yaml
allow: [example-general, example-low-cost, example-coding]
```

Use this pattern for coding agents, evaluations, or approved heavier workloads.

Model group names are deployment-defined. Names such as `default`, `fast`, `small`, `medium`, `high`, `big-coder`, and `vision` are examples from a reference or hosted deployment, not names required by the product.

Callers see only allowed groups when they call `/v1/models`. See [Available Models And Access](../getting-started/available-models) for the caller-facing behavior administrators should expect after issuing a token.

## Rotation

Generate a new router token, add its hashed caller entry to config, reload or restart the router, then move clients to the new token and mark the old caller token `status: rotated` before deleting it from active config. Reports continue to use the public token IDs and caller IDs captured on historical rows.

To suspend access without deleting history, set the caller token `status: suspended`, `disabled`, `expired`, or `rotated` and restart or reload the deployment. Disabling a user, project, or project membership should be done by removing or correcting active caller-token references first, because config validation requires active account references for all enabled caller tokens.

After each issue, rotation, or disablement, ask the caller to run `/v1/models`. That response is the source of truth for the model groups the caller token can request.

## Activation Boundary

Generating a token is not the same as activating it.

- **Self-hosted / file-owned installs:** use `metrum-ai-routerctl callers generate`
  (or `router-token-gen`) to create a mode-`0600` token file. Pass `--config` with
  `--write` to merge the hashed caller into local `config.yaml`, then reload or
  restart the router. Use `callers rotate` to replace the hash and emit a new token
  file, or `callers revoke` to disable a caller. The CLI never activates config on a
  remote managed hostname or signs licenses.

Never paste raw tokens, token hashes, or provider keys into tickets, chat, or public docs. Distribute the raw token once over an approved channel, then confirm access with `/v1/models`.
