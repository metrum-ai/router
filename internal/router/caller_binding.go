// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

func validateCallerBinding(cfg CallerBindingConfig, id string, serverTLS ServerTLSConfig, clientIP ClientIPConfig) error {
	mode := strings.TrimSpace(cfg.Mode)
	if mode == "" {
		mode = "off"
	}
	switch mode {
	case "off", "report", "enforce":
	default:
		return fmt.Errorf("caller %s binding.mode must be off, report or enforce", id)
	}
	if mode != "off" && len(cfg.AllowedCIDRs) == 0 && len(cfg.AllowedASNs) == 0 && cfg.MTLSSPIFFEID == "" && cfg.MTLSSubject == "" {
		return fmt.Errorf("caller %s binding requires at least one source constraint", id)
	}
	if mode == "enforce" && (cfg.MTLSSPIFFEID != "" || cfg.MTLSSubject != "") &&
		(strings.TrimSpace(serverTLS.Mode) != "router" || (strings.TrimSpace(serverTLS.ClientAuth) != "optional" && strings.TrimSpace(serverTLS.ClientAuth) != "require") || strings.TrimSpace(serverTLS.ClientCAFile) == "") {
		return fmt.Errorf("caller %s binding enforce requires server.tls client-auth verification and client_ca_file", id)
	}
	for _, raw := range cfg.AllowedCIDRs {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(raw)); err != nil {
			return fmt.Errorf("caller %s binding has invalid allowed_cidrs entry", id)
		}
	}
	for _, asn := range cfg.AllowedASNs {
		if asn == 0 {
			return fmt.Errorf("caller %s binding allowed_asns must be positive", id)
		}
	}
	if len(cfg.AllowedASNs) > 0 && len(clientIP.ASNPrefixes) == 0 {
		return fmt.Errorf("caller %s binding allowed_asns requires server.client_ip.asn_prefixes", id)
	}
	return nil
}

func validateASNPrefixes(prefixes []ASNPrefixConfig) error {
	seen := make(map[string]uint32, len(prefixes))
	for _, p := range prefixes {
		if p.ASN == 0 {
			return fmt.Errorf("server.client_ip.asn_prefixes ASN must be positive")
		}
		_, network, err := net.ParseCIDR(strings.TrimSpace(p.CIDR))
		if err != nil {
			return fmt.Errorf("server.client_ip.asn_prefixes contains invalid CIDR")
		}
		if previous, ok := seen[network.String()]; ok && previous != p.ASN {
			return fmt.Errorf("server.client_ip.asn_prefixes has conflicting ASN values for %s", network.String())
		}
		seen[network.String()] = p.ASN
	}
	return nil
}

func callerBindingMismatch(caller *callerRuntime, r *http.Request, ip clientIPInfo, config ClientIPConfig) string {
	b := caller.cfg.Binding
	if len(b.AllowedCIDRs) > 0 {
		address := net.ParseIP(ip.Address)
		matched := false
		for _, raw := range b.AllowedCIDRs {
			_, network, _ := net.ParseCIDR(strings.TrimSpace(raw))
			if address != nil && network.Contains(address) {
				matched = true
				break
			}
		}
		if !matched {
			return "cidr_mismatch"
		}
	}
	if len(b.AllowedASNs) > 0 {
		address := net.ParseIP(ip.Address)
		var asn uint32
		prefixBits := -1
		for _, p := range config.ASNPrefixes {
			_, network, _ := net.ParseCIDR(strings.TrimSpace(p.CIDR))
			if address != nil && network.Contains(address) {
				bits, _ := network.Mask.Size()
				if bits > prefixBits {
					asn, prefixBits = p.ASN, bits
				}
			}
		}
		matched := false
		for _, allowed := range b.AllowedASNs {
			if allowed == asn && asn != 0 {
				matched = true
				break
			}
		}
		if !matched {
			return "asn_mismatch"
		}
	}
	if b.MTLSSPIFFEID != "" || b.MTLSSubject != "" {
		if r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 {
			return "client_certificate_missing_or_unverified"
		}
		leaf := r.TLS.PeerCertificates[0]
		if !adminLeafExplicitClientAuth(leaf) {
			return "client_certificate_client_auth_required"
		}
		if b.MTLSSubject != "" && leaf.Subject.String() != b.MTLSSubject {
			return "client_certificate_subject_mismatch"
		}
		if b.MTLSSPIFFEID != "" {
			found := false
			uris, err := adminRawCertificateURISANs(leaf)
			if err != nil {
				return "client_certificate_spiffe_mismatch"
			}
			for _, uri := range uris {
				if uri == b.MTLSSPIFFEID {
					found = true
					break
				}
			}
			if !found {
				return "client_certificate_spiffe_mismatch"
			}
		}
	}
	return ""
}

func enforceCallerBinding(caller *callerRuntime, r *http.Request, ip clientIPInfo, config ClientIPConfig) error {
	if caller == nil {
		return nil
	}
	b := caller.cfg.Binding
	mode := strings.TrimSpace(b.Mode)
	if mode == "" || mode == "off" {
		return nil
	}
	reason := callerBindingMismatch(caller, r, ip, config)
	if reason == "" {
		return nil
	}
	if mode == "report" {
		log.Printf("caller binding mismatch caller_id=%q reason=%s", caller.cfg.ID, reason)
		return nil
	}
	return authPolicyError{status: http.StatusUnauthorized, code: "unauthorized", securityReason: reason}
}

// ServingTLSConfig adds the serving listener's configured client trust policy.
func ServingTLSConfig(base *tls.Config, cfg ServerTLSConfig) (*tls.Config, error) {
	if base == nil || (strings.TrimSpace(cfg.ClientAuth) != "optional" && strings.TrimSpace(cfg.ClientAuth) != "require") {
		return base, nil
	}
	data, err := os.ReadFile(cfg.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("server.tls.client_ca_file unavailable")
	}
	certs, err := decodeAdminClientCAs(data)
	if err != nil {
		return nil, fmt.Errorf("server.tls.client_ca_file invalid")
	}
	roots := x509.NewCertPool()
	for _, cert := range certs {
		roots.AddCert(cert)
	}
	out := base.Clone()
	if strings.TrimSpace(cfg.ClientAuth) == "require" {
		out.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		out.ClientAuth = tls.VerifyClientCertIfGiven
	}
	out.ClientCAs = roots
	return out, nil
}
