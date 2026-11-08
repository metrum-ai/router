// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/base64"
	"testing"
)

// Structural offline enrollment only; no activation, crypto proof, database
// Use/event acknowledgement or production emergency adapter is established.
func TestRuntimeMachineEmergencyDescriptorValidation(t *testing.T) {
	key := AdminMachineEmergencyCredentialConfig{ID: "emergency-one", Subject: "emergency:ops", Issuer: "offline", Kind: "api_key", KeyHashEnv: "TEST_OFFLINE_HASH", IssuedAt: "2026-11-07T00:00:00Z", ExpiresAt: "2026-11-07T01:00:00Z"}
	certificate := key
	certificate.ID = "emergency-cert"
	certificate.Kind = "certificate"
	certificate.KeyHashEnv = ""
	certificate.CertificateSHA256 = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	cases := []struct {
		name        string
		credentials []AdminMachineEmergencyCredentialConfig
		invalid     bool
	}{
		{name: "empty"}, {name: "key", credentials: []AdminMachineEmergencyCredentialConfig{key}}, {name: "certificate", credentials: []AdminMachineEmergencyCredentialConfig{certificate}},
		{name: "distinct classes", credentials: []AdminMachineEmergencyCredentialConfig{key, certificate}},
		{name: "duplicate ID", credentials: []AdminMachineEmergencyCredentialConfig{key, key}, invalid: true},
	}
	modify := func(name string, original AdminMachineEmergencyCredentialConfig, fn func(*AdminMachineEmergencyCredentialConfig)) {
		fn(&original)
		cases = append(cases, struct {
			name        string
			credentials []AdminMachineEmergencyCredentialConfig
			invalid     bool
		}{name, []AdminMachineEmergencyCredentialConfig{original}, true})
	}
	modify("missing verifier", key, func(c *AdminMachineEmergencyCredentialConfig) { c.KeyHashEnv = "" })
	modify("mixed proof classes", key, func(c *AdminMachineEmergencyCredentialConfig) { c.CertificateSHA256 = certificate.CertificateSHA256 })
	modify("missing issued", key, func(c *AdminMachineEmergencyCredentialConfig) { c.IssuedAt = "" })
	modify("relative expiry", key, func(c *AdminMachineEmergencyCredentialConfig) { c.ExpiresAt = "1h" })
	modify("overlong", key, func(c *AdminMachineEmergencyCredentialConfig) { c.ExpiresAt = "2026-11-07T01:00:01Z" })
	modify("reversed times", key, func(c *AdminMachineEmergencyCredentialConfig) { c.ExpiresAt = c.IssuedAt })
	modify("unknown kind", key, func(c *AdminMachineEmergencyCredentialConfig) { c.Kind = "bearer" })
	modify("empty subject", key, func(c *AdminMachineEmergencyCredentialConfig) { c.Subject = "" })
	modify("empty issuer", key, func(c *AdminMachineEmergencyCredentialConfig) { c.Issuer = "" })
	modify("padded pin", certificate, func(c *AdminMachineEmergencyCredentialConfig) { c.CertificateSHA256 += "=" })
	modify("short pin", certificate, func(c *AdminMachineEmergencyCredentialConfig) { c.CertificateSHA256 = "AAAA" })
	duplicate := certificate
	duplicate.ID = "different-id"
	cases = append(cases, struct {
		name        string
		credentials []AdminMachineEmergencyCredentialConfig
		invalid     bool
	}{"duplicate pin", []AdminMachineEmergencyCredentialConfig{certificate, duplicate}, true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := prepareAdminMachineRuntimeSettings(AdminMachineAuthenticationConfig{Emergency: AdminMachineEmergencyConfig{Credentials: tc.credentials}}, "")
			if (err != nil) != tc.invalid {
				t.Fatalf("descriptor error=%v want invalid=%v", err, tc.invalid)
			}
		})
	}
}
