<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Admin mTLS trust domains

> Integration status: the trust-domain configuration and verification components
> are part of the draft machine-authentication integration. Listener bootstrap,
> production audit adapters and complete configuration/CLI integration are still
> pending. This is not a production-readiness guide for the current binary.

The admin control plane is separate from the serving port. Its certificates
prove machine identity or sender possession, not browser sessions. A successful
identity still requires the operation's sole `Authorizer.Authorize` decision.
This configuration does not change serving-port certificate or browser behavior.

## Explicit authorities

Configure each independent certificate authority set under
`admin.authentication.mtls.trust_domains`:

```yaml
admin:
  authentication:
    mtls:
      reload_interval: 30s
      trust_domains:
        - issuer: corp-a
          ca_files: [/etc/router/admin/corp-a.pem]
          subjects:
            - san: 'uri:spiffe://workloads.example/automation'
              subject: automation-a
        - issuer: corp-b
          ca_files: [/etc/router/admin/corp-b.pem]
          subjects:
            - san: 'uri:spiffe://workloads.example/automation'
              subject: automation-b
```

`issuer` is an explicitly configured trust-domain identifier. It is **never**
read from certificate issuer/subject DNs, Common Name, URI host, DNS name, or
forwarded certificate headers. The native client chain must verify under exactly
one configured domain's current roots before that domain's SAN mappings apply.
Thus the same signed SAN can map to different subjects under different roots.
Unmapped extra SANs are ignored; conflicting mapped subjects deny authentication.
URI SAN matching uses the signed bytes exactly; DNS matching is case-insensitive.
Common Name, email and unmapped SANs do not establish an identity.

Only explicit CA files are used; system roots are not added. Bundles must contain
strict certificate PEM with valid CA/basic-constraint/certSign attributes. Files,
bytes and certificate counts are bounded. A malformed block cannot be skipped
in favor of a later valid anchor.

## Unique roots and ambiguity

Duplicate issuer names are configuration errors. A root appearing in two domains
is also a configuration error, comparing **both exact certificate DER and SPKI**.
Reissuing or cross-signing a root certificate with the same public key does not
make it a separate authority. Keep that root/key in one domain. These checks run
at startup and every configuration/file reload, before any replacement publishes.

Distinct root keys can still admit a cross-signed intermediate chain under more
than one domain. Certificate-only identity then fails with
`ErrInvalidCredential`; mapping order cannot resolve authority ambiguity. Adjust
the trust sets rather than relying on first-match SAN or issuer ordering.

## Flat compatibility form

The flat form describes exactly **one implicit domain**. Its identifier comes
from the explicit `issuer` configuration value:

```yaml
admin:
  authentication:
    mtls:
      issuer: corp-a
      ca_files: [/etc/router/admin/corp-a.pem]
      subjects:
        - san: 'dns:automation.example'
          subject: automation-a
```

Do not combine flat `ca_files` with `trust_domains`; mixed configuration is an
error. No issuer identifier is inferred from certificate contents or filenames.
An empty subject map may provide possession-only roots, but cannot authenticate
a certificate-only identity.

## Certificate-bound tokens

RFC 8705 `cnf.x5t#S256` possession is checked against the **union of configured
roots**. The verified token issuer and claim mapping supply identity; the client
certificate supplies possession and does not need a separate SAN identity map.
The binding root source must be the same union used by the native TLS listener.
Certificate-identity authority ambiguity does not itself invalidate token-bound
possession under that union.

Both uses require a currently valid verified native client chain and explicit
ClientAuth EKU. Missing EKU, Any EKU, and ClientAuth combined with Any are rejected;
ClientAuth combined with ServerAuth is allowed after successful chain checking.
Proxy/forwarded headers never manufacture certificate possession.

## Reload contract

A replacement is built and validated in full: domain roots, issuer-qualified SAN
maps, root inventories and the listener/token-possession union publish together.
A missing/malformed file, duplicate authority/root key or mixed configuration
retains the complete last-good publication and emits no successful reload hook.
Published pools are immutable and stable until the next valid publication.

Per-request revalidation checks current roots, including requests on an existing
TLS connection. Publication checks during verification fail closed on a changed
snapshot. They are not atomic fencing with later authorization/dispatch and do
not cancel operations already past their accepted boundary. Joint runtime and
authorization-generation integration remains separate work.

See [the configuration fragment](../examples/admin-mtls-trust-domains.fragment.yaml).

## Request generation and finite timeout

Every admin request captures one immutable authentication bundle (credential
catalogs, trust roots, sender state and its compatible authorization snapshot)
exactly once, before credential validation. Later mount, handler, guard and
contract recheck decisions use that same captured authority; there is no second
lookup and no credential re-proof. Reload or CA rotation publishes one complete
replacement bundle atomically. A request that captured generation N keeps
completing under N after N+1 publishes — this is bounded overlap, not a lease,
admission gate or dispatch fence.

Admin requests run with a finite 60-second context deadline, and the listener's
default `limits.write_timeout_ms` is also 60000. The context deadline stops
cooperative work (JWKS fetch, discovery, sender proof, database calls that honor
context) and the write timeout bounds the response. Neither force-terminates an
uncooperative handler's side effects, so the timeout bounds served responses
rather than guaranteeing instantaneous revocation for work already past its
accepted boundary.
