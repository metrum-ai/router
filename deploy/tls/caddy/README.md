# Reference: Caddy in front of the router (proxy-TLS mode)

This is the reference **proxy-TLS** deployment for the router. Caddy terminates
TLS for the public hostname, obtains and renews the certificate over ACME
**DNS-01** through a DNS provider module, and forwards traffic to the router
listeners on loopback. Nothing has to be reachable from the internet for the
challenge, which is what makes a private or NAT-ed host workable.

The primary recipe uses **DigitalOcean** DNS, with a complete Cloudflare variant
(`Caddyfile.cloudflare`) and notes for Route 53. Router-native TLS
(`server.tls.mode: acme` on the router itself) is the alternative when the router
should hold the certificate; see
[Automated TLS Certificates](../../../docs-site/docs/configuration/tls.md). Pick
one owner for the certificate per hostname — do not run both against the same
name.

| piece | owner |
| --- | --- |
| certificate, renewal, OCSP | Caddy |
| public DNS record (optional, via `create-dns-record.sh`) | your automation or the router |
| admin listener (`127.0.0.1:8081`) | router |
| serving listener (`127.0.0.1:8080`) | router |

## 1. Build Caddy with a DNS provider module

Caddy's DNS-01 support lives in provider modules that are compiled in. Build a
Caddy binary with the module for your provider:

```bash
go install github.com/caddyserver/xcaddy@latest
# DigitalOcean (the primary Caddyfile):
xcaddy build --with github.com/caddy-dns/digitalocean --output ./caddy
# or Cloudflare (the Caddyfile.cloudflare variant):
xcaddy build --with github.com/caddy-dns/cloudflare --output ./caddy
# or Route 53:
xcaddy build --with github.com/caddy-dns/route53 --output ./caddy
```

Pin the module and Caddy versions in your image build and record them in your
change log. The scripts are run with `bash` below so a checkout without the
executable bit still works.

## 2. DigitalOcean walkthrough

**Create a scoped token.** In the DigitalOcean control panel, create a personal
access token with a custom scope: `domain` read, and create/update/delete on
records in the zone you will use (DNS-scoped only — it needs no droplet or
billing rights). Export it as `DO_AUTH_TOKEN`; the scripts read it from the
environment and never put it in a command line.

**Create the DNS record.** Caddy validates over DNS, so the hostname must already
resolve to the host that runs Caddy. `create-dns-record.sh` creates or updates
that record, idempotently, and only for records it owns:

```bash
PROVIDER=digitalocean DO_AUTH_TOKEN=... ZONE=example.com \
  NAME=router.example.com TYPE=A VALUE=203.0.113.10 OWNER=router-prod-1 \
  bash ./create-dns-record.sh
```

- A second run with the same value writes nothing.
- The first run claims an ownership TXT record at `_metrum-router.<name>`,
  creates the record, then binds the marker to the record's provider id
  (`metrum-ai-router-managed=1;owner=...;id=<record-id>`).
- A record that already exists without that marker, a marker naming another
  owner, and a record whose id no longer matches the marker (deleted and
  recreated by hand) are **never** modified: the script exits `3` and says why.
  This is the same rule, and the same marker format, as the router's own
  `server.tls.acme.dns.record` manager, so the two can share a zone.
- Exit codes: `0` changed or already correct, `2` usage, `3` ownership
  conflict, `4` provider or transport error.
- The Authorization header is written to curl's stdin, so the token never appears
  in the process argument list; a test asserts this by recording curl's argv.

The DigitalOcean v2 equivalent by hand is `doctl compute domain records create
--domain example.com --record-type A --record-name router --record-data
203.0.113.10 --record-ttl 300`, plus the same `_metrum-router.router` TXT
marker if you want the router to recognise the record as managed.

**Run Caddy.** Start the router with its listeners on loopback (the defaults):

```yaml
server:
  listen: 127.0.0.1:8080
```

```bash
ROUTER_DNS_PROVIDER=digitalocean \
ROUTER_HOSTNAME=router.example.com \
ROUTER_ACME_EMAIL=ops@example.com \
CADDY_DATA_DIR=/var/lib/caddy \
DO_AUTH_TOKEN=... \
  bash ./smoke-test.sh ./caddy
```

`smoke-test.sh` validates the Caddyfile, starts Caddy, waits for the certificate
over DNS-01, prints its issuer and expiry, and stops Caddy. For the long-running
deployment use the same environment with `./caddy run --config Caddyfile`.

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

## 3. Cloudflare variant

Build the Cloudflare module, then use the same flow with the Cloudflare file and
credential:

```bash
xcaddy build --with github.com/caddy-dns/cloudflare --output ./caddy

PROVIDER=cloudflare CF_API_TOKEN=... ZONE=example.com \
  NAME=router.example.com TYPE=A VALUE=203.0.113.10 OWNER=router-prod-1 \
  bash ./create-dns-record.sh
```

`Caddyfile.cloudflare` is otherwise identical to `Caddyfile`; use it with
`./caddy run --config Caddyfile.cloudflare`, and `ROUTER_DNS_PROVIDER=cloudflare
bash ./smoke-test.sh ./caddy` for the smoke test. A scoped token with Zone:Read
and DNS:Edit is enough; the global key plus email also works but is not
recommended.

## 4. Verify

```bash
curl -fsS https://router.example.com/healthz
openssl s_client -connect router.example.com:443 -servername router.example.com </dev/null 2>/dev/null \
  | openssl x509 -noout -issuer -subject -dates
```

Caddy renews at roughly two thirds of the certificate lifetime and reloads
without a restart. Its certificate store lives under `CADDY_DATA_DIR`.

## Tests

The record script's ownership rules, idempotency and token handling are covered
by an offline test that runs against fake DigitalOcean and Cloudflare APIs with
no network:

```bash
python3 deploy/tls/caddy/test/test_create_dns_record.py
```

For both providers it asserts the exact writes (claim marker, create record,
bind marker to the record id), that a re-run makes zero write calls, that an
in-place update happens on a value change, that a foreign record, another
owner's marker and a replaced record all exit `3` without writing, and that the
token never appears in curl's argv.

Both Caddyfiles are `caddy fmt` clean and were checked with `caddy validate`
against a Caddy built with the matching DNS module.

## Limits

- The proxy path does not give the router the client certificate: if you need
  mTLS client identity, terminate mTLS at the router (admin listener) or have
  Caddy verify the client and forward the verdict — that mapping belongs to the
  machine-authentication work, not this recipe.
- The recipe does not manage the DNS record for you unless you run
  `create-dns-record.sh` (or enable `server.tls.acme.dns.record`, which works
  with router-native TLS).
- Caddy's own certificate store and the router's `server.tls` cache directory
  are separate; back up the one that is in use.
