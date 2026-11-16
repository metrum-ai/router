# Admin Plane Upgrade and Operator Guide

Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0

This guide covers the v4.7 → v4.8 admin control-plane change: the admin listener, its exposure
options, client credentials and rotation, the metrics move, the legacy-route flag, and the
Kubernetes isolation overlay. It is written for the v4.8.0 release; the versioned `/admin/v1`
operations arrive from the contract work and are the migration target for every legacy route.

The control plane is machine-to-machine only: no HTTP Basic, no cookies, no browser sessions and no
HTML. The human browser reports UI is not part of the control plane; it stays on the serving port
with its current login and is unaffected by this guide.

## 1. What v4.8.0 changes

| Area | v4.7 | v4.8.0 |
| --- | --- | --- |
| Admin transport | `/admin/*` on the serving port | second listener (`admin.listen`, default `127.0.0.1:8081`); v4.8 temporarily retains machine routes on the serving port behind a flag |
| Admin credentials | Basic, OIDC sessions, admin API keys | machine credentials on the admin listener (admin API keys in v4.8.0, mTLS/OAuth/federation as the authentication work lands); the serving-port browser login is unchanged for the reports UI |
| `/metrics` | serving port, caller token with `metrics` `read` | admin listener, authorized for an admin identity with `metrics` `read`; `admin.metrics.on_serving_port: true` keeps the v4.7 scrape |
| Legacy `/admin/*` | undecorated | `Deprecation`, `Sunset` and a `Link` to `/admin/v1` on every machine admin response |
| Exposure | whatever the serving port uses | loopback by default; a non-loopback bind needs TLS or an explicit plaintext override |

A configuration without an `admin:` block starts the loopback admin listener by default. Set
`admin.enabled: false` explicitly to keep a single listener.
Configuring only `admin.unix_socket.path` disables the default TCP admin listener; set
`admin.listen` explicitly to keep TCP access as well.

## v5.0.0 control-plane schema 17

Schema 17 adds the admin client registry and credential bindings to the router-config control-plane
database, and adds client ID and actor fields to credential lifecycle events. The migration is online
and depends on schema 15. Use the packaged non-serving migration job and the backup, apply, verify,
and status steps in [Data Migrations](DATA_MIGRATIONS.md); do not start serving replicas until the
required migration contract is current. Client endpoints return unavailable until the client tables
exist. Credential bindings match configured admin identities; disabled or deleted clients and
revoked or expired bindings cannot authenticate. Re-register replacement credentials to restore a
disabled client. A deleted client ID cannot be re-enabled; create a new client ID instead.

## 2. Exposure options

**Loopback only (default).** `admin.enabled: true` with the default `127.0.0.1:8081`. Reach it from
the host, a sidecar container or `kubectl port-forward`. When several replicas share one host, give
each replica a unique `admin.listen`; the default address can be bound only once on that host:

```bash
kubectl -n <namespace> port-forward deploy/<router-deployment> 8081:8081
curl -fsS http://127.0.0.1:8081/healthz
```

**Unix socket.** Add `admin.unix_socket.path` to expose the same authenticated control plane locally.
When it is the only listener configured, the admin plane starts without a TCP listener; set
`admin.listen` as well to retain TCP access.
The default socket mode is `0600`; set `owner` to a numeric UID when the router must hand the socket
to a local service account, and set `mode` to an octal permission string when needed. Filesystem
permissions gate connection, while the same machine credential and authorization checks still gate
each request. Use `metrum-ai-routerctl ... --socket /run/metrum/admin.sock --admin-key-file <file>`
instead of `--router-url` for API-backed commands. Existing non-socket paths and permission errors
fail startup; shutdown removes the socket created by that process.

**Router-native TLS.** The router serves its own certificate. Mount the certificate and key from a
Secret and point the listener at them:

```yaml
admin:
  enabled: true
  listen: 0.0.0.0:8443
  external_url: https://router-admin.internal.example.com
  tls:
    mode: router
    cert_file: /app/tls/admin.crt
    key_file: /app/tls/admin.key
    min_version: "1.3"
```

Changing `cert_file`/`key_file` needs a restart; the client CA bundle reloads live.

**mTLS.** Add a client CA bundle and require client certificates. Keep `client_auth: optional` while
API-key or token clients still need to connect; `require` fails the handshake without a certificate.
A wrong or unknown CA always fails the handshake:

```yaml
admin:
  tls:
    mode: router
    cert_file: /app/tls/admin.crt
    key_file: /app/tls/admin.key
    client_ca_file: /app/tls/client-ca.crt
    client_auth: optional
```

**Reverse-proxy TLS.** A trusted proxy terminates TLS. List the proxy addresses; the listener then
requires `X-Forwarded-Proto: https` from those peers and rejects other non-loopback peers. A reverse
proxy cannot prove a client certificate, so mTLS requires `mode: router`:

```yaml
admin:
  enabled: true
  listen: 0.0.0.0:8081
  external_url: https://router-admin.internal.example.com
  tls:
    mode: proxy
    trusted_proxy_cidrs: ["10.0.0.0/8"]
```

**Kubernetes.** `deploy/kubernetes/overlays/admin-plane` starts the listener on the pod IP, exposes
it through a private `ClusterIP` Service named `metrum-ai-router-admin` with no Ingress, adds a
default-deny ingress NetworkPolicy and allows the admin port only from pods labelled
`access.metrum.ai/admin-client: "true"`:

```bash
kubectl apply -k deploy/kubernetes/overlays/admin-plane
kubectl -n <namespace> rollout status deploy/metrum-ai-router
kubectl -n <namespace> get svc metrum-ai-router-admin
```

Selected clients (an operator jump pod, a CI runner namespace) label themselves:

```bash
kubectl -n <namespace> label pod <client-pod> access.metrum.ai/admin-client=true
```

## 3. Client credentials

**Admin API keys** are the v4.8.0 machine credential. Generate the key once, store only the
verifier, and keep the raw value in the client's secret store:

```bash
metrum-ai-routerctl adminkeys generate --key-id ops-automation --token-out /secure/path/ops.key
```

The command prints the `key_hash_env` snippet for the router configuration. The raw key
(`rtr_admin_<key_id>_<secret>`) is shown once; the router stores only the PBKDF2 verifier named by
`key_hash_env`.

Rotation with overlap:

1. Generate a second key with a new `key_id` and a subject/domain/permission set identical to the
   first.
2. Add it to `server.admin_auth.api_keys`, roll the deployment, and confirm the client authenticates
   with the new key.
3. Move automation to the new key.
4. Set `status: disabled` on the old key (or remove it) and roll again.

A disabled key never falls through to another mechanism. `routerctl` refuses to take a raw key as a
flag value; use the file or environment form the command documents.

The `/admin/v1` resource API adds separate Casbin objects for providers, catalog,
projects, memberships, quotas, budgets, and retention. Review the new grants in
`ADMIN_API_KEYS.md`; the older `admin:config` grant does not authorize these
resources. Retention updates that can enable deletion or purge more data require
`admin:retention:force`. Budget trip state is re-evaluated against the current
ceiling on each admission, so raising or removing a ceiling restores admission
without waiting for the next UTC window. `routerctl admin-resource` provides
HTTP access to the versioned resource endpoints.

Before enabling asynchronous operations, backups, or restores, apply the
non-serving usage and router-config migrations through schema 23 and schema 19, respectively, using the
two-scope Job in [DATA_MIGRATIONS.md](DATA_MIGRATIONS.md). The restore API
requires the separate `admin:restore:write` grant. It updates the configuration
and usage databases sequentially; it is not an atomic cross-database operation.

**Break-glass access (v4.8.0).** There is no unauthenticated admin route. Prepare one sealed admin
key with `admin`-level grants, held offline by the on-call owner, and use it only to restore normal
credentials; rotate it immediately afterwards. The offline emergency-credential activation flow is
part of the machine-authentication work and is not a v4.8.0 dependency for this guide.

**Browser admin access** for the reports UI is unchanged: keep the existing
`server.admin_auth.basic`/`oidc` configuration and login on the serving port. Those credentials never
authorize the admin listener.

## 4. Metrics move

With the plane enabled and no override, `/metrics` is served on the admin listener and the serving
port returns `404`. Scrape it with an admin identity that has `metrics` `read`:

```bash
curl -fsS -H "Authorization: Bearer $(cat /secure/path/ops.key)" \
  http://127.0.0.1:8081/metrics
```

To keep the v4.7 scrape (caller token with `metrics_admin` on the serving port), set:

```yaml
admin:
  metrics:
    on_serving_port: true
```

A caller token is rejected on the admin listener; an admin key without a `metrics` `read` grant is
also rejected. Do not configure `on_serving_port: false` while the plane is disabled: validation
rejects it because metrics would be served nowhere.

## 5. v5 serving-port route removal

v5.0.0 removes machine `/admin/*` routes from the serving listener. Move automation to the
versioned `/admin/v1/*` API on the dedicated admin listener before upgrading. The removed
`admin.legacy_routes_on_serving_port` and `admin.legacy_sunset` keys must not appear in file
configuration; v5 config loading rejects either key with migration guidance. The reports UI and
`/admin/auth/*` browser session routes remain on the serving port.

For database-mode deployments, run the v5 router-config migration maintenance step before starting
the router. It removes only those two obsolete keys from stored canonical configs. Back up the
configuration database first and follow the v5.0.0 section of the `docs-site/docs/release-notes/upgrade-guide.md`.

## 6. API and CLI parity

`metrum-ai-routerctl` online commands use `/admin/v1` in v5.0.0. Config import, export, activate,
and rollback use the Admin API by default; add `--offline` only when the router is stopped and the
operator intentionally accesses the control-plane database. Other online operations use the Admin
API directly. Set either `--router-url`/`ROUTER_URL` for TCP or `--socket` for a Unix socket;
the options cannot be combined. When neither is set, routerctl uses the restored default
`http://127.0.0.1:8081`. A socket can be the only listener or run alongside TCP. Plain HTTP
is accepted only on loopback; remote endpoints require HTTPS. Admin credentials come from
`ROUTER_ADMIN_KEY`, `ROUTER_ADMIN_KEY_FILE`, or
`--admin-key-file`.

| Operation | Versioned API | Router CLI | Notes |
| --- | --- | --- | --- |
| List model groups | `GET /admin/v1/config/groups` | `groups list --router-url <url>` |
| Show a group | `GET /admin/v1/config/groups/{group}` | `groups show <group>` | |
| Replace group targets | `PUT /admin/v1/config/groups/{group}/targets` | `groups set-targets <group>` | |
| Add / remove a target | `POST` / `DELETE …/targets` | `groups add-target`, `groups remove-target` | |
| Validate targets | `POST …/targets:validate` | — | API only |
| Roll back a group | `POST …/rollback` | `groups rollback <group>` | |
| Group history | `GET …/history` | `groups history <group>` | |
| Shadow run | `POST/DELETE …/shadow`, `POST …/shadow/promote` | `groups shadow start|promote|stop` | |
| Config sets | `GET/POST /admin/v1/config/sets…`, `POST …/activate`, `POST /admin/v1/config/rollback` | `config import|export|activate|rollback` | Online by default; `--offline` explicitly selects DB access |
| Caller directory | `GET/POST /admin/v1/callers…` | `callers generate`, `callers hash` (offline) | Online issue/rotate/revoke are API only |
| Admin keys | admin-key API (follow-up release) | `adminkeys generate` (offline) | Raw key is never returned by an API |
| Reports and exports | `/admin/v1/reports/...` | `reports cfo [--format json|csv|markdown] [--since …] [--reports-prefix /admin/v1/reports]` | Old `/admin/reports` prefix is rejected; browser UI stays on the serving port |
| Metrics | `GET /admin/v1/metrics`, `GET /metrics` on the admin port | — | |
| Health | `GET /healthz`, `GET /readyz` on the admin port | — | State only: `{"ok":true}` |

## 7. Compliance exports

Admin reports and security exports keep their existing objects and permissions and are re-mounted
under `/admin/v1/reports`. Export with an identity that holds `admin:reports` `read`/`export` (or
`admin:security_reports` for security exports):

```bash
curl -fsS -H "Authorization: Bearer $(cat /secure/path/ops.key)" \
  "https://<admin-endpoint>/admin/v1/reports/export.csv?since=2026-11-01" -o report.csv
```

Keep the export credentials distinct from inference caller tokens, and store exported files under
the deployment's data-handling policy.

## 8. Upgrade runbook: v4.7 → v4.8 and v5.0.0

1. **Record the current surface.** Capture the scrape configuration for `/metrics`, the routes in
   use under `/admin/*`, and the browser login settings.
2. **Upgrade without adopting the plane.** Deploy the v4.8.0 image with the unchanged configuration.
   Without an `admin:` block nothing moves: same routes, same `/metrics`, same decisions.
3. **Decide exposure.** Loopback, router TLS, proxy TLS or the Kubernetes overlay from §2. Bind
   remote only with TLS in front of it.
4. **Move metrics first.** Either set `metrics.on_serving_port: true` to keep the current scrape, or
   point the scrape at the admin listener with a key that holds `metrics` `read`.
5. **Enable the plane.** Set `admin.enabled: true` with the chosen exposure and roll out. The admin
   port logs nothing but state at `/healthz`; the process fails fast if the plane cannot bind.
6. **Issue a machine credential** (§3) and run one read-only operation through it, for example a
   config status read.
7. **Move automation** to `/admin/v1` on the admin listener and verify each client against the new
   endpoint before upgrading to v5.0.0. v5 has no legacy-route toggle; requests to removed machine
   paths on the serving port return `404`.
8. **Rollback** to v4.8 requires restoring the prior binary and its compatible configuration from
   the deployment backup. Do not add the removed keys to a v5 configuration. Inference remains on
   the serving listener if the admin plane is stopped.

## 9. Kubernetes image security

For v5.0.0, run Harbor task images as UID/GID 10001. Kubernetes manifest image references now include both a release tag placeholder and a digest placeholder; replace them with the exact tag and digest published for your chosen architecture. Third-party serving image references use immutable digests. Do not deploy mutable tags alone.

The optional `deploy/kubernetes/trusted-registry-policy.example.yaml` shows a Kyverno policy that requires approved router or vLLM registries and digests for regular, init, and ephemeral containers. It is an example only; the router does not install or require an admission controller. Adapt the allowed images and registry hosts to your cluster before applying it. Trivy's KSV-0125 suppression is limited to exact rendered examples because its static registry list cannot establish deployment-specific trust.

## 10. Kubernetes network verification

CI renders this overlay with `kustomize` and scans it with trivy; there is no cluster test harness in
CI, so the following is a **manual** check on a real cluster. Run it after every change to the
overlay.

```bash
kubectl apply -k deploy/kubernetes/overlays/admin-plane
kubectl -n <namespace> wait --for=condition=ready pod -l app.kubernetes.io/name=metrum-ai-router --timeout=5m
python3 scripts/check_admin_plane_manifests.py

# 1. The admin Service is private.
kubectl -n <namespace> get svc metrum-ai-router-admin -o jsonpath='{.spec.type}'   # ClusterIP
kubectl -n <namespace> get ingress | grep metrum-ai-router-admin || echo "no ingress selects the admin service"

# 2. An unselected pod cannot reach the admin port.
kubectl -n <namespace> run unselected-client --rm -it --restart=Never --image=curlimages/curl -- \
  sh -c 'curl -m 3 -fsS http://metrum-ai-router-admin:8081/healthz' ; echo "exit=$?"   # non-zero

# 3. A selected pod can.
kubectl -n <namespace> run selected-client --rm -it --restart=Never --image=curlimages/curl \
  --labels=access.metrum.ai/admin-client=true -- \
  sh -c 'curl -m 3 -fsS http://metrum-ai-router-admin:8081/healthz'                    # {"ok":true}

# 4. The serving port still answers.
kubectl -n <namespace> run serving-client --rm -it --restart=Never --image=curlimages/curl -- \
  sh -c 'curl -m 3 -fsS http://metrum-ai-router:80/healthz'
```

The admin port admits exactly one kind of source: a pod labelled
`access.metrum.ai/admin-client: "true"` **in a named namespace**. The check refuses a `namespaceSelector` with
no labels (any namespace), the admin-client label without a namespace match, and any rule that mixes the
admin-client selector with another source. A deployment that must reach the admin port from a network range
(for example a reviewed bastion subnet) instead of a labelled pod records it in
`BROAD_IPBLOCK_OVERRIDES` in `scripts/check_admin_plane_manifests.py` as `cidr -> reason`; the reason is
printed on every run, ranges wider than `/16` (IPv4) or `/48` (IPv6) need that entry, and `0.0.0.0/0` is never
allowed.

A public Ingress cannot reach the admin Service because no Ingress selects it; the NetworkPolicy
would also drop the traffic. Delete the test pods (`kubectl -n <namespace> delete pod
unselected-client selected-client serving-client`) when finished.

## 10. Limits

- TLS certificate and key rotation needs a restart; the client CA bundle reloads live.
- No Ingress, NodePort or LoadBalancer service is supported for the admin plane.
- A Unix-socket admin listener is not available.
- Use the current `/admin/v1` API reference for operations available in v5.0.0.
- Plaintext in the Kubernetes overlay is acceptable only with the default-deny NetworkPolicy; use
  router-native TLS for anything less isolated.


## v5.0.0 migration: serving-port admin routes

v5.0.0 removes the legacy machine `/admin/*` routes from the serving port, without an override flag
or sunset date. Move automation to `/admin/v1` on the admin listener before upgrading. The listener
now defaults to enabled on loopback at `127.0.0.1:8081` and requires authentication; set
`admin.enabled: false` to keep one listener. A configuration with no `admin:` block receives these
defaults.

Remove `admin.legacy_routes_on_serving_port` and `admin.legacy_sunset` from configuration. Either
key causes config loading to fail with a migration message. `/admin/reports/*` and `/admin/auth/*`
remain on the serving port.
