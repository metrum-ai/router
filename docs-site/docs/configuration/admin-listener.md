---
title: Admin Listener
doc_type: reference
---

# Admin Listener

The admin control plane runs on its own HTTP listener, separate from inference traffic. It is
machine-to-machine only: it accepts no HTTP Basic, no cookies and no browser sessions, and it serves
no HTML or UI assets. The human browser reports UI stays on the serving port with its current login
and is not part of the control plane.

The listener is opt-in. A configuration without an `admin:` block behaves exactly like v4.7: one
listener, the legacy `/admin/*` routes on the serving port and `/metrics` on the serving port.

## Configuration

```yaml title="config.example.yaml"
admin:
  enabled: false
  listen: 127.0.0.1:8081
  allow_plaintext_non_loopback: false
  legacy_routes_on_serving_port: true
  legacy_sunset: "2027-03-01T00:00:00Z"
  external_url: ""
  tls:
    mode: off
    certificate_source: files
    cert_file: ""
    key_file: ""
    # acme:
    #   domains: [admin.example.com]
    #   email: ops@example.com
    #   directory_url: https://acme-staging-v02.api.letsencrypt.org/directory
    #   accept_tos: true
    #   cache_dir: /var/lib/router/acme
    min_version: "1.2"
    client_ca_file: ""
    client_auth: none
    trusted_proxy_cidrs: []
  limits:
    max_body_bytes: 1048576
    max_header_bytes: 65536
    read_header_timeout_ms: 5000
    read_timeout_ms: 30000
    write_timeout_ms: 60000
    idle_timeout_ms: 60000
    shutdown_timeout_ms: 10000
  metrics:
    # on_serving_port: true
```

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Starts the second listener. `false` keeps the v4.7 surface. |
| `listen` | `127.0.0.1:8081` | Listener address. Loopback by default; a remote bind is an explicit opt-in. |
| `allow_plaintext_non_loopback` | `false` | Allows a non-loopback bind without TLS. It logs a startup warning. |
| `legacy_routes_on_serving_port` | `true` | Keeps the legacy machine `/admin/*` routes on the serving port with `Deprecation`/`Sunset` headers. It never covers the reports UI. |
| `legacy_sunset` | `2027-03-01T00:00:00Z` | `Sunset` header value. It must be at least 90 days after the v4.8.0 release. |
| `external_url` | unset | Absolute base URL of the plane. The only source of absolute request URLs; the router never derives them from `Host` or `X-Forwarded-*`. |
| `tls.mode` | `off` | `off`, `router` (the router serves its own certificate) or `proxy` (a reverse proxy terminates TLS). |
| `tls.certificate_source` | `files` | `files` (serve `cert_file`/`key_file`) or `acme` (automated issuance; requires `tls.mode: router` and an `tls.acme` block). |
| `tls.cert_file`, `tls.key_file` | unset | Required for `tls.mode: router` with `certificate_source: files`. Changing them requires a restart. |
| `tls.acme` | unset | Automated issuance settings for `certificate_source: acme` (`domains`, `email`, `directory_url`, `accept_tos`, `cache_dir`, DNS provider and renewal keys). Issuance and renewal are covered by the automated-certificate work (#810); the listener only consumes the resulting certificate through its single handshake hook. |
| `tls.min_version` | `1.2` | Minimum negotiated TLS version: `1.2` or `1.3`. |
| `tls.client_ca_file` | unset | Client CA bundle for mTLS. |
| `tls.client_auth` | `none` | `none`, `optional` or `require`. `optional` verifies a presented certificate and still lets API-key and token clients connect; `require` fails the handshake without one. A wrong or unknown CA always fails the handshake. |
| `tls.trusted_proxy_cidrs` | `[]` | Required for `tls.mode: proxy`. Non-loopback peers outside the list are rejected. |
| `limits.*` | see above | Admin-only body, header and connection deadlines, independent of the serving listener and its streaming settings. |
| `metrics.on_serving_port` | unset | Keeps the v4.7 serving-port `/metrics` scrape with caller-token authorization. `/metrics` stays there until the admin listener serves it (admin identity with `metrics` read); moving it earlier is refused at startup. |

## Exposure options

1. **Loopback only (default).** The listener binds `127.0.0.1:8081` and is reachable from the host,
   a sidecar or a local `kubectl port-forward`. No certificate is needed.
2. **Router-native TLS.** Set `tls.mode: router` with `certificate_source: files` and `cert_file`,
   `key_file`, or with `certificate_source: acme` plus an `acme` block, and bind the address the
   machine clients reach. In Kubernetes bind the pod IP behind a private Service, never behind a
   public Ingress.
3. **Reverse-proxy TLS.** Set `tls.mode: proxy` and list the proxy addresses in
   `tls.trusted_proxy_cidrs`. The listener then requires `X-Forwarded-Proto: https` from those
   proxies and rejects every other non-loopback peer. A reverse proxy cannot prove a client
   certificate, so mTLS is available only with `tls.mode: router`.

A non-loopback bind with `tls.mode: off` refuses to start unless
`allow_plaintext_non_loopback: true` is set explicitly.

## What the listener serves

- its own `GET /healthz` and `GET /readyz`, which report plane state only;
- `/metrics` once the admin-side handler lands (until then the serving port keeps the v4.7 scrape);
- the versioned `/admin/v1` operations (the control-plane contract).

The listener returns `404` for inference paths (`/v1/*`, `/anthropic/v1/*`), the docs site, the
reports UI and every unknown path, with an `application/problem+json` body. An unsupported method on
a mounted `/admin/v1` operation returns `405`. The serving socket returns `404` for `/admin/v1/*`.

## Hardening

Every admin response carries `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`,
`Content-Security-Policy: default-src 'none'; frame-ancestors 'none'; base-uri 'none'`,
`X-Frame-Options: DENY` and `Referrer-Policy: no-referrer`. The listener never emits
`Access-Control-Allow-*` headers and answers CORS preflights with `405`. Request bodies above
`limits.max_body_bytes` are refused with `413`. Requests carrying a cookie, HTTP Basic or any
non-Bearer authorization scheme are refused with `401` before any handler runs.

Two refusals happen below the router, in the HTTP server itself, and therefore carry a plain-text
body rather than a problem document: `431` when the request header block exceeds
`limits.max_header_bytes`, and `400` for a malformed request line or header block. Treat both as
transport-level rejections; no handler, credential check or authorization decision runs for them.

## Lifecycle

The admin listener starts after the serving listener and drains independently:
`limits.shutdown_timeout_ms` bounds its drain, and a failed or stopped admin listener never changes
serving readiness. An admin bind failure at startup stops the process, because a plane the operator
enabled must not be missing silently.

## Migrating from v4.7

1. Leave `admin.enabled: false` to keep the current surface, or set it to `true` to start the
   listener.
2. If anything scrapes `/metrics` from the serving port with a caller token, set
   `admin.metrics.on_serving_port: true` before enabling the plane, or move the scrape to the admin
   listener.
3. Move automation to `/admin/v1`. The legacy machine routes stay on the serving port while
   `legacy_routes_on_serving_port` is `true` and advertise `Deprecation` and `Sunset` headers.
4. The browser reports UI is unchanged: it stays on the serving port with its current login.

## Limits

- TLS certificate and key changes require a restart in v4.8; only the client CA bundle reloads live.
- ACME issuance runs outside the listener; the listener verifies the SNI and fetches the certificate
  through one handshake hook.
- A Unix-socket admin listener is not available.
- The listener does not issue or renew certificates; supply them or terminate TLS at a proxy.
