// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type adminMachineIssuerSettings struct {
	profile           adminMachineJWTConfig
	minimumConstraint string
}
type adminMachineRuntimeSettings struct {
	jwksCacheTTL, jwksRefreshMinInterval, jwksRequestTimeout time.Duration
	dpopProofMaxAge                                          time.Duration
	dpopReplayCapacity                                       int
	apiKeysEnabled                                           bool
	issuers                                                  []adminMachineIssuerSettings
}

func adminMachineSettingDuration(raw, label string, fallback, min, max time.Duration) (time.Duration, error) {
	value := fallback
	if raw != "" {
		var err error
		value, err = time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("admin.authentication.%s duration invalid", label)
		}
	}
	if value < min || value > max {
		return 0, fmt.Errorf("admin.authentication.%s duration out of bounds", label)
	}
	return value, nil
}

// Pure, bounded startup validation. No discovery, verifier resolution, durable
// activation, replay allocation or identity acceptance occurs here. The factory
// must consume these results rather than accepting settings and ignoring them.
func prepareAdminMachineRuntimeSettings(c AdminMachineAuthenticationConfig, externalURL string) (adminMachineRuntimeSettings, error) {
	var s adminMachineRuntimeSettings
	var err error
	s.apiKeysEnabled = c.APIKeys.Enabled == nil || *c.APIKeys.Enabled
	if s.jwksCacheTTL, err = adminMachineSettingDuration(c.JWKS.CacheTTL, "jwks.cache_ttl", 5*time.Minute, time.Second, time.Hour); err != nil {
		return s, err
	}
	if s.jwksRefreshMinInterval, err = adminMachineSettingDuration(c.JWKS.RefreshMinInterval, "jwks.refresh_min_interval", 30*time.Second, time.Second, time.Hour); err != nil {
		return s, err
	}
	if s.jwksRequestTimeout, err = adminMachineSettingDuration(c.JWKS.RequestTimeout, "jwks.request_timeout", 5*time.Second, time.Millisecond, 30*time.Second); err != nil {
		return s, err
	}
	if s.jwksRefreshMinInterval > s.jwksCacheTTL {
		return s, errors.New("admin.authentication JWKS refresh interval exceeds cache lifetime")
	}
	if s.dpopProofMaxAge, err = adminMachineSettingDuration(c.DPoP.ProofMaxAge, "dpop.proof_max_age", time.Minute, time.Second, 5*time.Minute); err != nil {
		return s, err
	}
	s.dpopReplayCapacity = c.DPoP.ReplayCapacity
	if s.dpopReplayCapacity == 0 {
		s.dpopReplayCapacity = 10000
	}
	if s.dpopReplayCapacity < 1 || s.dpopReplayCapacity > 100000 {
		return s, errors.New("admin.authentication DPoP replay capacity invalid")
	}
	if len(c.Issuers) > 32 {
		return s, errors.New("admin.authentication issuer count exceeded")
	}
	seen := map[string]bool{}
	mappings := 0
	for _, i := range c.Issuers {
		if len(i.IssuerURL) > 2048 || !machineIssuerEndpoint(i.IssuerURL, false) {
			return s, errors.New("admin.authentication HTTPS issuer required")
		}
		u, _ := url.Parse(i.IssuerURL)
		if u.RawQuery != "" || u.ForceQuery || seen[i.IssuerURL] {
			return s, errors.New("admin.authentication issuer query or duplicate forbidden")
		}
		seen[i.IssuerURL] = true
		kind := i.Kind
		if kind == "" {
			kind = "oauth"
		}
		if kind != "oauth" && kind != "federated" {
			return s, errors.New("admin.authentication issuer kind invalid")
		}
		if strings.TrimSpace(i.Audience) == "" || len(i.Audience) > 2048 {
			return s, errors.New("admin.authentication audience required")
		}
		maxLife, err := adminMachineSettingDuration(i.MaxLifetime, "issuer.max_lifetime", time.Hour, time.Second, 24*time.Hour)
		if err != nil {
			return s, err
		}
		skew, err := adminMachineSettingDuration(i.ClockSkew, "issuer.clock_skew", 30*time.Second, 0, time.Minute)
		if err != nil {
			return s, err
		}
		algs := append([]string(nil), i.Algorithms...)
		if len(algs) == 0 {
			algs = []string{"RS256", "ES256"}
		}
		if len(algs) > 2 {
			return s, errors.New("admin.authentication algorithm count invalid")
		}
		seenAlg := map[string]bool{}
		for _, alg := range algs {
			if (alg != "RS256" && alg != "ES256") || seenAlg[alg] {
				return s, errors.New("admin.authentication asymmetric algorithm allowlist invalid")
			}
			seenAlg[alg] = true
		}
		if len(i.AllowedJWKSHosts) > 16 {
			return s, errors.New("admin.authentication JWKS authority count exceeded")
		}
		hosts := append([]string(nil), i.AllowedJWKSHosts...)
		for _, host := range hosts {
			if _, ok := machineJWTAllowedHost(host); !ok {
				return s, errors.New("admin.authentication JWKS authority invalid")
			}
		}
		minimum := i.SenderConstraint
		if minimum == "" {
			minimum = "none"
		}
		switch minimum {
		case "none":
		case "mtls":
			if !adminMTLSHasCAFiles(c.MTLS) {
				return s, errors.New("admin.authentication mTLS sender constraint requires trust domains")
			}
		case "dpop":
			u, err := url.Parse(externalURL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
				return s, errors.New("admin.authentication DPoP requires trusted HTTPS external_url")
			}
			if _, err := normalizeAdminDPoPURI(externalURL); err != nil {
				return s, errors.New("admin.authentication DPoP external_url invalid")
			}
		default:
			return s, errors.New("admin.authentication sender constraint invalid")
		}
		mappings += len(i.Subjects)
		if len(i.Subjects) == 0 || mappings > 4096 {
			return s, errors.New("admin.authentication subject mapping count invalid")
		}
		subjects := make([]adminMachineJWTSubject, 0, len(i.Subjects))
		ids := map[string]string{}
		for _, m := range i.Subjects {
			if (m.Claim != "sub" && m.Claim != "client_id") || m.Value == "" || len(m.Value) > 2048 || len(m.Subject) > 1024 || !validMachinePrincipal(Principal{Subject: m.Subject, Kind: kind, CredentialID: "mapping", Issuer: i.IssuerURL}) {
				return s, errors.New("admin.authentication immutable subject mapping invalid")
			}
			id := m.Claim + "\x00" + m.Value
			if old, ok := ids[id]; ok && old != m.Subject {
				return s, errors.New("admin.authentication subject mapping ambiguous")
			}
			ids[id] = m.Subject
			subjects = append(subjects, adminMachineJWTSubject{Claim: m.Claim, Value: m.Value, Subject: m.Subject})
		}
		s.issuers = append(s.issuers, adminMachineIssuerSettings{profile: adminMachineJWTConfig{Issuer: i.IssuerURL, Audience: i.Audience, Kind: kind, Algorithms: algs, MaxLifetime: maxLife, ClockSkew: skew, clockSkewExplicit: i.ClockSkew != "", JWKSCacheTTL: s.jwksCacheTTL, JWKSRefreshMinInterval: s.jwksRefreshMinInterval, JWKSRequestTimeout: s.jwksRequestTimeout, Subjects: subjects, AllowedJWKSHosts: hosts, AllowPrivateNetwork: i.AllowPrivateNetwork, AllowLegacyTokenType: i.AllowLegacyTokenType}, minimumConstraint: minimum})
	}
	if err := validateAdminMachineEmergencyDescriptors(c.Emergency.Credentials); err != nil {
		return s, err
	}
	return s, nil
}

func validateAdminMachineEmergencyDescriptors(credentials []AdminMachineEmergencyCredentialConfig) error {
	if len(credentials) > 32 {
		return errors.New("admin.authentication emergency enrollment limit exceeded")
	}
	ids := map[string]bool{}
	pins := map[string]bool{}
	for _, c := range credentials {
		if !validAdminKeyID(c.ID) || ids[c.ID] || len(c.Subject) > 1024 || len(c.Issuer) > 2048 || strings.TrimSpace(c.Subject) == "" || strings.TrimSpace(c.Subject) != c.Subject || strings.TrimSpace(c.Issuer) == "" || strings.TrimSpace(c.Issuer) != c.Issuer || !validMachinePrincipal(Principal{Subject: c.Subject, Kind: "emergency", CredentialID: c.ID, Issuer: c.Issuer}) {
			return errors.New("admin.authentication emergency identity invalid")
		}
		ids[c.ID] = true
		issued, e1 := time.Parse(time.RFC3339Nano, c.IssuedAt)
		expires, e2 := time.Parse(time.RFC3339Nano, c.ExpiresAt)
		if e1 != nil || e2 != nil || issued.IsZero() || !expires.After(issued) || expires.Sub(issued) > time.Hour {
			return errors.New("admin.authentication emergency absolute lifetime invalid")
		}
		switch c.Kind {
		case "api_key":
			if c.KeyHashEnv == "" || len(c.KeyHashEnv) > 256 || strings.TrimSpace(c.KeyHashEnv) != c.KeyHashEnv || c.CertificateSHA256 != "" {
				return errors.New("admin.authentication emergency verifier reference invalid")
			}
		case "certificate":
			pin, err := base64.RawURLEncoding.DecodeString(c.CertificateSHA256)
			if err != nil || len(pin) != 32 || base64.RawURLEncoding.EncodeToString(pin) != c.CertificateSHA256 || pins[c.CertificateSHA256] || c.KeyHashEnv != "" {
				return errors.New("admin.authentication emergency certificate pin invalid")
			}
			pins[c.CertificateSHA256] = true
		default:
			return errors.New("admin.authentication emergency credential kind invalid")
		}
	}
	return nil
}
