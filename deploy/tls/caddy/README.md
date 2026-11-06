# Reference: Caddy in front of the router (proxy-TLS mode)

This is the reference **proxy-TLS** deployment for the router. Caddy terminates
TLS for the public hostname, obtains and renews the certificate over ACME
**DNS-01** through a DNS provider module, and forwards traffic to the router
listeners on loopback. Nothing has to be reachable from the internet for the
challenge, which is what makes a private or NAT-ed host workable.

Router-native TLS (`server.tls.mode: acme`) is the alternative when the router
itself should hold the certificate; see
[Automated TLS Certificates](../../../docs-site/docs/configuration/tls.md). Pick
one owner for the certificate per hostname — do not run both against the same
name.

| piece | owner |
| --- | --- |
| certificate, renewal, OCSP | Caddy |
| DNS record (optional) | `create-dns-record.sh`, or the router's own manager when it terminates TLS itself |
| public DNS record (optional, via `create-dns-record.sh`) | your automation or the router |
| admin listener (`127.0.0.1:8081`) | router |
| serving listener (`127.0.0.1:8080`) | router |

## 1. Build Caddy with a DNS provider module

Caddy's DNS-01 support lives in provider modules that are compiled in. Build a
Caddy binary with the module for your provider:

```bash
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
xcaddy build \
  --with github.com/caddy-dns/cloudflare \
  --output ./caddy
```

`github.com/caddy-dns/route53` and `github.com/caddy-dns/digitalocean` work the
same way. Pin the module and Caddy versions in your image build; record them in
your change log.

## 2. Create the DNS record

Caddy validates over DNS, so the hostname must already resolve to the host that
runs Caddy. `create-dns-record.sh` creates or updates that record through the
Cloudflare API, idempotently, and only for records it owns:

```bash
CF_API_TOKEN=... ZONE=example.com NAME=router.example.com TYPE=A \
  VALUE=203.0.113.10 OWNER=router-prod-1 ./create-dns-record.sh
```

- The first run claims the ownership TXT record at `_metrum-router.<name>`,
  creates the record, then binds the marker to the record's provider id
  (`metrum-ai-router-managed=1;owner=...;id=<record-id>`). A second run with the
  same value writes nothing.
- A record that already exists without that marker, a marker that names a
  different owner, and a record whose id no longer matches the marker (deleted
  and recreated by hand) are **never** modified: the script exits `3` and says
  why. This is the same rule, and the same marker format, as the router's own
  `server.tls.dns.record` manager, so the two can share a zone and a record.
- Exit codes: `0` changed or already correct, `2` usage, `3` ownership
  conflict, `4` provider or transport error.

The script's idempotency and conflict rules are covered by a test that runs
against a fake provider API with no network:

```bash
python3 deploy/tls/caddy/test/test_create_dns_record.py
```

## 3. Run Caddy

Start the router with its listeners on loopback (the defaults):

```yaml
server:
  listen: 127.0.0.1:8080
```

```bash
ROUTER_HOSTNAME=router.example.com \
ROUTER_ACME_EMAIL=ops@example.com \
CADDY_DATA_DIR=/var/lib/caddy \
CF_DNS_API_TOKEN=... \
  ./caddy run --config Caddyfile
```

The Caddyfile sends `/admin/v1/*` to `ROUTER_ADMIN_ADDR` (default
`127.0.0.1:8081`) and everything else to `ROUTER_SERVE_ADDR` (default
`127.0.0.1:8080`), setting `X-Forwarded-Proto`, `X-Forwarded-Host`,
`X-Forwarded-For` and passing `X-Request-Id` through. HSTS, `nosniff` and a
no-referrer policy are set at the proxy.

Because the router sees a plaintext connection from the proxy, tell it to trust
that proxy for forwarded headers:

```yaml
server:
  client_ip:
    trusted_proxy_cidrs:
      - 127.0.0.1/32
```

Only that address may set the forwarded headers; never widen it to `0.0.0.0/0`.

## 4. Verify

```bash
curl -fsS https://router.example.com/healthz
openssl s_client -connect router.example.com:443 -servername router.example.com </dev/null 2>/dev/null \
  | openssl x509 -noout -issuer -subject -dates
```

Caddy renews at roughly two thirds of the certificate lifetime and reloads
without a restart. Its certificate store lives under `CADDY_DATA_DIR`.

## Smoke test

`smoke-test.sh` is the documented end-to-end check for this recipe. It validates
the Caddyfile, starts Caddy against a chosen ACME directory with the DNS-01
module, waits for the certificate to appear, prints its issuer and expiry, and
cleans up. It needs real DNS provider credentials and a hostname whose zone you
control, so it runs on an operator machine, not in CI:

```bash
ROUTER_HOSTNAME=router-test.example.com \
ROUTER_ACME_EMAIL=ops@example.com \
ROUTER_ACME_CA=https://acme-staging-v02.api.letsencrypt.org/directory \
CF_DNS_API_TOKEN=... \
  ./smoke-test.sh ./caddy
```

For a local ACME test CA, run Pebble plus `pebble-challtestsrv`, point
`ROUTER_ACME_CA` at `https://127.0.0.1:14000/dir`, set
`ROUTER_ACME_CA_ROOT` to Pebble's root certificate and add
`--config Caddyfile.pebble`. Pebble must resolve DNS through
`pebble-challtestsrv`; the DNS provider module still writes to the real provider
API, so the Pebble path needs a provider you can point at a test zone (or a
provider module built against a local API endpoint).

## Limits

- The proxy path does not give the router the client certificate: if you need
  mTLS client identity, terminate mTLS at the router (admin listener) or have
  Caddy verify the client and forward the verdict — that mapping belongs to the
  machine-authentication work, not this recipe.
- The recipe does not manage the DNS record for you unless you run
  `create-dns-record.sh` (or enable `server.tls.dns.record`, which only works
  with router-native TLS).
- Caddy's own certificate store and the router's `server.tls.cache_dir` are
  separate; back up the one that is in use.
