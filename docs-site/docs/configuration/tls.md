---
title: Automated TLS Certificates
doc_type: howto
---

# Automated TLS Certificates

The router can terminate TLS on its own listener with a certificate it loads
from disk or obtains and renews automatically. Certificates come from any RFC
8555 CA — Let's Encrypt by default — through a **DNS-01** challenge, so the host
needs no inbound port for validation: a loopback-only, private or NAT-ed
deployment can still hold a publicly trusted certificate.

TLS is **off by default**. Without a `server.tls` block the listener serves
plaintext exactly as in v4.7, and nothing else in your configuration changes.

```yaml
server:
  tls:
    mode: off # off (default) or router
```

`mode: router` means this router terminates TLS itself. The admin listener has a
third mode, `proxy`, where a reverse proxy terminates TLS and the router holds
no certificate; that mode belongs to the admin-plane configuration.

## Modes and certificate sources

| key | values | meaning |
| --- | --- | --- |
| `mode` | `off`, `router` | `off` serves plaintext (v4.7 behaviour); `router` terminates TLS here. |
| `certificate_source` | `files`, `acme` | Inside `router` mode: serve a static certificate, or obtain and renew one over ACME. Default `files`. |
| `cert_file`, `key_file` | paths | The static certificate for `certificate_source: files`. |
| `min_version` | `1.2`, `1.3` | TLS floor; default `1.2`. |

In `router` mode the certificate is ready **before** the listener binds, and a
TLS-configured listener never falls back to plaintext. With nothing usable
cached, issuance must succeed or startup fails. When a valid certificate is
already cached it is served immediately and the renewal retries in the
background, so an unavailable CA delays a renewal rather than a restart.

## Serving an existing certificate

```yaml
server:
  tls:
    mode: router
    certificate_source: files
    cert_file: /etc/metrum-ai-router/tls/server.crt
    key_file: /etc/metrum-ai-router/tls/server.key
    acme:
      domains:
        - router.example.com
```

The certificate must cover every configured domain, and both files are checked
at startup: an expired certificate fails startup, and the key must not be group-
or world-readable. The router never renews a `files` certificate; renew it out
of band and restart, or move to `certificate_source: acme`.

## Automated certificates with ACME

```yaml
server:
  tls:
    mode: router
    certificate_source: acme
    min_version: "1.2"
    acme:
      domains:
        - router.example.com
      email: ops@example.com
      accept_tos: true
      directory_url: https://acme-v02.api.letsencrypt.org/directory
      cache_dir: /var/lib/metrum-ai-router/tls
      dns:
        provider: cloudflare
        zone: example.com
        credentials:
          api_token_env: ROUTER_DNS_API_TOKEN
```

- `domains` is one SAN order. `_acme-challenge.<domain>` is created through the
  DNS provider and removed after validation.
- `email` is the ACME account contact. `accept_tos: true` is the explicit
  consent to the CA's terms of service and is required.
- `directory_url` must be `https`. The exception,
  `allow_insecure_directory: true`, exists for local test CAs and must not be
  used in production.
- `cache_dir` holds the certificate, its private key and the ACME account key
  with owner-only permissions. It is created with mode `0700` and tightened if
  it is group- or world-readable.

Optional settings and their defaults:

```yaml
server:
  tls:
    acme:
      key_type: ecdsa_p256 # or rsa_2048
      startup_timeout_seconds: 120 # bound on the first issuance
      reload_interval_seconds: 30 # shared-store polling on replicas
      renewal:
        window_ratio: 0.6667 # renew at two thirds of the certificate lifetime
        jitter_seconds: 900 # displace each deadline by up to this much
        retry_max_seconds: 3600 # ceiling of the renewal retry backoff
        use_ari: true # honour the CA's RFC 9773 renewal window when offered
```

The renewal deadline is two thirds of the certificate lifetime, displaced by
jitter capped at a tenth of that lifetime. A failed renewal keeps the current
certificate in service, raises a metric and retries with exponential backoff
from 30 seconds to `retry_max_seconds`. When the CA advertises renewal
information (ARI), the CA's suggested window replaces the ratio; any ARI error
falls back to the ratio.

## DNS providers

DNS-01 is issued through one of three providers. Every credential is a
**secret reference**: `<field>_env` names an environment variable,
`<field>_file` names a file. Literal credentials in the configuration are
rejected, and resolved values are scrubbed from errors and logs.

| provider | credentials | notes |
| --- | --- | --- |
| `cloudflare` | `api_token_env` (Zone:Read + DNS:Edit), or `api_key_env` + `email_env` | A scoped token is preferred. |
| `digitalocean` | `token_env` | |
| `route53` | `access_key_id_env` + `secret_access_key_env` (+ optional `session_token_env`), or `use_ambient_credentials: true` | On EKS, prefer ambient credentials (IRSA) and no static keys. |

```yaml
server:
  tls:
    acme:
      dns:
        provider: route53
        zone: example.com # optional: longest matching zone of the account
        ttl_seconds: 60
        propagation_timeout_seconds: 120
        propagation_nameservers: [] # optional, e.g. ["10.0.0.53:53"] for split-horizon DNS
        credentials:
          use_ambient_credentials: true
```

When `zone` is set it must be a suffix of every domain; otherwise the longest
matching zone of the account is used. A domain that matches no zone fails
issuance.

## Managing the DNS record

The router can also keep the record that points your hostname at it in sync.
Record management is **off by default** and only ever touches records it
created, which it tracks with an ownership TXT record:

```yaml
server:
  tls:
    acme:
      dns:
        record:
          manage: true
          type: A # or AAAA, or CNAME
          value: <public-ipv4-or-target> # the address the hostname should resolve to
          ttl_seconds: 300
          owner: router-prod-1 # default: the host name
```

Every managed record gets a companion TXT record at `_metrum-router.<name>` with
a value such as
`metrum-ai-router-managed=1;owner=router-prod-1;name=router.example.com;type=A;id=<record-id>`.
The marker also records the provider id of the record it owns. A re-run with the
same value writes nothing. A record that already exists without that marker —
somebody else's record — is never modified or deleted, and neither is a record
whose id no longer matches the marker (deleted and recreated by hand): both are
reported with a metric and an error log line.

## The admin listener

`admin.tls` uses the same certificate vocabulary with `mode: off|router|proxy`,
`cert_file`, `key_file`, `certificate_source` and the `acme` sub-block, plus the
client-authentication keys described in
[Admin Authentication](./admin-authentication). One PKI decision per hostname:
run router-native ACME on the listener that owns the public name, and leave the
other listener on `off` or `proxy`.

## Metrics

`/metrics` exports, for each TLS listener:

```
metrum_ai_router_tls_certificate_expiry_timestamp_seconds
metrum_ai_router_tls_certificate_not_before_timestamp_seconds
metrum_ai_router_tls_certificate_renewal_due_timestamp_seconds
metrum_ai_router_tls_certificate_valid
metrum_ai_router_tls_acme_orders_total{result}
metrum_ai_router_tls_renewal_failures_total{reason}
metrum_ai_router_tls_dns_present_failures_total{provider}
metrum_ai_router_tls_dns_conflict_total{provider,reason}
metrum_ai_router_tls_dns_records_managed{provider,type}
metrum_ai_router_tls_issuer_lock_failures_total
metrum_ai_router_tls_ari_failures_total
```

Alert on `metrum_ai_router_tls_renewal_failures_total` and on
`metrum_ai_router_tls_certificate_expiry_timestamp_seconds - time() < 14 days`.
A renewal failure first fires while at least a third of the lifetime remains.

## Multiple replicas

Replicas that share `cache_dir` (a shared volume) cooperate: a POSIX file lock
lets exactly one replica issue, the others serve the cached certificate and
pick up the renewed one within `reload_interval_seconds`. Per-replica issuance
is not supported. If the filesystem cannot provide the lock, the replica logs
it, raises `metrum_ai_router_tls_issuer_lock_failures_total` and refuses to
issue rather than racing another replica.

## Proxy TLS mode

Many deployments terminate TLS in a reverse proxy instead. The reference recipe
lives in
[`deploy/tls/caddy/`](https://github.com/metrum-ai/router/tree/main/deploy/tls/caddy):
a Caddy build with a DNS-01 provider module in front of the router's loopback
listeners, an idempotent `create-dns-record.sh` that only touches records it
owns (same ownership-marker rule and format as the router's own record
manager), and a documented `smoke-test.sh` for an operator run against a staging
CA.

Leave `server.tls.mode: off` in that mode: the proxy owns the certificate. Tell
the router to trust the proxy for forwarded headers through
`server.client_ip.trusted_proxy_cidrs`, and keep that list to the proxy address.

The recipe's `smoke-test.sh` needs real DNS provider credentials and a hostname
whose zone you control, so it is a manual operator check rather than a CI job;
the router-native path is what the automated Pebble suite exercises.

## Limits

- Only DNS-01 is implemented: no HTTP-01 or TLS-ALPN-01, and no OCSP stapling.
- No zone or NS creation: record management only creates, updates and removes
  records it owns.
- A `files` certificate is reloaded by restart only.
- The live check against a production CA needs a real DNS zone and hostname and
  is an operator step; the automated tests run against the Pebble test CA.
