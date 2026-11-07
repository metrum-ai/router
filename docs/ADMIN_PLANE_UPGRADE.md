# Admin Plane Upgrade and Operator Guide

Copyright 2026 Metrum AI

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
| Admin transport | `/admin/*` on the serving port | opt-in second listener (`admin.listen`, default `127.0.0.1:8081`), plus the legacy routes on the serving port behind a flag |
| Admin credentials | Basic, OIDC sessions, admin API keys | machine credentials on the admin listener (admin API keys in v4.8.0, mTLS/OAuth/federation as the authentication work lands); the serving-port browser login is unchanged for the reports UI |
| `/metrics` | serving port, caller token with `metrics` `read` | admin listener, authorized for an admin identity with `metrics` `read`; `admin.metrics.on_serving_port: true` keeps the v4.7 scrape |
| Legacy `/admin/*` | undecorated | `Deprecation`, `Sunset` and a `Link` to `/admin/v1` on every machine admin response |
| Exposure | whatever the serving port uses | loopback by default; a non-loopback bind needs TLS or an explicit plaintext override |

A configuration without an `admin:` block behaves exactly like v4.7. Nothing in this guide is
required to keep an existing deployment running; it is required to adopt the control plane.

## 2. Exposure options

**Loopback only (default).** `admin.enabled: true` with the default `127.0.0.1:8081`. Reach it from
the host, a sidecar container or `kubectl port-forward`:

```bash
kubectl -n <namespace> port-forward deploy/<router-deployment> 8081:8081
curl -fsS http://127.0.0.1:8081/healthz
```

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

## 5. Legacy routes and the deprecation window

While `admin.legacy_routes_on_serving_port` is `true` (the v4.8.0 default) the machine admin routes
stay on the serving port and answer with:

- `Deprecation: true`
- `Sunset: Tue, 01 Mar 2027 00:00:00 GMT` (configurable with `admin.legacy_sunset`, at least 90 days
  after the v4.8.0 release)
- `Link: </admin/v1/...>; rel="deprecation"`

The flag covers machine routes only: `/admin/config/*`, `/admin/callers*` and `/admin/training/*`.
The browser reports UI (`/admin/reports*`) and the `/admin/auth/*` session routes keep working on the
serving port with their current login, even with the flag off. Set the flag to `false` once every
automation client uses `/admin/v1`.

## 6. API and CLI parity

`metrum-ai-routerctl` talks to the legacy HTTP paths today; the online commands move to `/admin/v1`
in v5.0. Use this table to pick the API path for new automation.

| Operation | Versioned API | Router CLI | Notes |
| --- | --- | --- | --- |
| List model groups | `GET /admin/v1/config/groups` | `groups list --router-url <url>` | CLI uses the legacy path until v5.0 |
| Show a group | `GET /admin/v1/config/groups/{group}` | `groups show <group>` | |
| Replace group targets | `PUT /admin/v1/config/groups/{group}/targets` | `groups set-targets <group>` | |
| Add / remove a target | `POST` / `DELETE …/targets` | `groups add-target`, `groups remove-target` | |
| Validate targets | `POST …/targets:validate` | — | API only |
| Roll back a group | `POST …/rollback` | `groups rollback <group>` | |
| Group history | `GET …/history` | `groups history <group>` | |
| Shadow run | `POST/DELETE …/shadow`, `POST …/shadow/promote` | `groups shadow start|promote|stop` | |
| Config sets | `GET/POST /admin/v1/config/sets…` | `config import`, `config activate` (offline DB) | Online set operations are API only |
| Caller directory | `GET/POST /admin/v1/callers…` | `callers generate`, `callers hash` (offline) | Online issue/rotate/revoke are API only |
| Admin keys | admin-key API (follow-up release) | `adminkeys generate` (offline) | Raw key is never returned by an API |
| Reports and exports | `/admin/v1/reports/...` | `reports` (read-only) | Browser UI stays on the serving port |
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

## 8. Upgrade runbook: v4.7 → v4.8

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
7. **Move automation** to `/admin/v1`, then set `admin.legacy_routes_on_serving_port: false` and roll
   out again. Watch for residual clients hitting the legacy paths.
8. **Rollback** is the reverse: re-enable the legacy flag and either disable the plane or restore
   `metrics.on_serving_port`. The serving port never depends on the admin listener, so stopping the
   plane cannot affect inference.

## 9. Kubernetes network verification

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

A public Ingress cannot reach the admin Service because no Ingress selects it; the NetworkPolicy
would also drop the traffic. Delete the test pods (`kubectl -n <namespace> delete pod
unselected-client selected-client serving-client`) when finished.

## 10. Limits

- TLS certificate and key rotation needs a restart; the client CA bundle reloads live.
- No Ingress, NodePort or LoadBalancer service is supported for the admin plane.
- A Unix-socket admin listener is not available in v4.8.0.
- `/admin/v1` operations beyond the ones listed in §6 land with the contract work; the parity table is
  updated with them.
- Plaintext in the Kubernetes overlay is acceptable only with the default-deny NetworkPolicy; use
  router-native TLS for anything less isolated.
