<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Caller identity binding

Caller source binding adds network and workload checks after token authentication. Existing callers default to `off`.

```yaml
server:
  tls:
    mode: router
    certificate_source: files
    cert_file: /etc/router/server.pem
    key_file: /etc/router/server-key.pem
    client_auth: optional
    client_ca_file: /etc/router/client-ca.pem
  client_ip:
    trusted_proxy_cidrs: [10.0.0.0/8]
    asn_prefixes:
      - cidr: 198.51.100.0/24
        asn: 64500
callers:
  - id: build-agent
    binding:
      mode: enforce
      allowed_cidrs: [10.42.0.0/16]
      allowed_asns: [64500]
      mtls_spiffe_id: spiffe://corp/ns/agents/sa/build-agent
```

`allowed_cidrs` matches the address resolved by `server.client_ip`, including its trusted-proxy rules. `allowed_asns` uses the longest matching prefix in the operator-maintained ASN-to-prefix map at `server.client_ip.asn_prefixes`; the router does not look up an ASN database. Update the map as address allocations change. An unmapped address does not match an ASN allow list.

`mtls_spiffe_id` matches an exact URI SAN. `mtls_subject` matches the exact `pkix.Name.String()` subject. If both are set, both must match. The router checks only the TLS peer certificate, requires a verified chain, and requires the client-auth extended key usage. It ignores forwarded certificate headers. TLS must terminate at the router, or pass through the load balancer unchanged. Use `client_auth: optional` to request and verify certificates while allowing unbound callers without one; an enforced caller still needs its matching certificate. Use `require` when every connection must present a client certificate.

Modes are `off`, `report`, and `enforce`. `report` logs only the caller ID and a fixed mismatch reason, then continues. `enforce` rejects mismatches with the same generic HTTP 401 response used for invalid tokens; the security event records the specific binding failure reason. Enforced certificate binding requires `server.tls.mode: router`, client-certificate verification (`client_auth: optional` or `require`), and a valid `client_ca_file`; config validation rejects missing settings. A TLS-terminating ingress cannot provide the certificate identity to the router, so use CIDR/ASN binding there.
