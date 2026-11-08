// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func TestRuntimeEmergencyCertificateCatalog(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	pin := func(s string) string {
		digest := sha256.Sum256([]byte(s))
		return base64.RawURLEncoding.EncodeToString(digest[:])
	}
	cfg := adminEmergencyCertificateConfig{id: "rescue-cert", subject: "emergency:rescue", issuer: "local", thumbprint: pin("leaf"), issuedAt: now, expiresAt: now.Add(time.Hour)}
	catalog, err := newAdminEmergencyCertificateCatalog([]adminEmergencyCertificateConfig{cfg}, nil, nil)
	if err != nil {
		t.Fatal("maximum one-hour trusted enrollment rejected", err)
	}
	original, ok := catalog.credential(cfg.id)
	if !ok {
		t.Fatal("missing trusted descriptor")
	}
	for _, mode := range []string{"empty-id", "bad-id", "empty-subject", "reserved-subject", "empty-issuer", "padding", "bad-pin", "zero-issued", "zero-expiry", "no-lifetime", "over-hour", "reserved-disabled", "duplicate-id", "duplicate-pin"} {
		t.Run(mode, func(t *testing.T) {
			bad := cfg
			reserved := map[string]bool{}
			configs := []adminEmergencyCertificateConfig{bad}
			switch mode {
			case "empty-id":
				bad.id = ""
			case "bad-id":
				bad.id = "not an id"
			case "empty-subject":
				bad.subject = ""
			case "reserved-subject":
				bad.subject = securityAdministratorRole
			case "empty-issuer":
				bad.issuer = ""
			case "padding":
				bad.thumbprint += "="
			case "bad-pin":
				bad.thumbprint = "invalid"
			case "zero-issued":
				bad.issuedAt = time.Time{}
			case "zero-expiry":
				bad.expiresAt = time.Time{}
			case "no-lifetime":
				bad.expiresAt = bad.issuedAt
			case "over-hour":
				bad.expiresAt = bad.issuedAt.Add(time.Hour + time.Nanosecond)
			case "reserved-disabled":
				reserved[bad.id] = false
			case "duplicate-id":
				other := cfg
				other.thumbprint = pin("other")
				configs = append(configs, other)
			case "duplicate-pin":
				other := cfg
				other.id = "second-cert"
				configs = append(configs, other)
			}
			configs[0] = bad
			if _, err := newAdminEmergencyCertificateCatalog(configs, reserved, nil); err == nil {
				t.Fatal("invalid enrollment accepted")
			}
		})
	}
	// Every authority/lifetime replacement changes the durable activation binding.
	for _, mode := range []string{"id", "subject", "issuer", "pin", "issued", "expiry"} {
		t.Run("binding-"+mode, func(t *testing.T) {
			changed := cfg
			switch mode {
			case "id":
				changed.id = "different-cert"
			case "subject":
				changed.subject = "different"
			case "issuer":
				changed.issuer = "different"
			case "pin":
				changed.thumbprint = pin("different")
			case "issued":
				changed.issuedAt = changed.issuedAt.Add(time.Second)
			case "expiry":
				changed.expiresAt = changed.expiresAt.Add(-time.Second)
			}
			replacement, err := newAdminEmergencyCertificateCatalog([]adminEmergencyCertificateConfig{changed}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, ok := replacement.credential(changed.id)
			if !ok || descriptor.Fingerprint == original.Fingerprint {
				t.Fatal("replacement retained old activation binding")
			}
		})
	}
	configs := make([]adminEmergencyCertificateConfig, 32)
	for i := range configs {
		configs[i] = cfg
		configs[i].id = fmt.Sprintf("cert-%d", i)
		configs[i].thumbprint = pin(configs[i].id)
	}
	full, err := newAdminEmergencyCertificateCatalog(configs, nil, nil)
	if err != nil {
		t.Fatal("32 bounded enrollments rejected", err)
	}
	configs[0].subject = "caller-mutated"
	descriptor, ok := full.credential("cert-0")
	if !ok || descriptor.Subject != cfg.subject {
		t.Fatal("catalog retained caller-owned configuration")
	}
	descriptor.Subject = "returned-mutated"
	again, _ := full.credential("cert-0")
	if again.Subject != cfg.subject {
		t.Fatal("returned descriptor mutated catalog")
	}
	if _, err := newAdminEmergencyCertificateCatalog(append(configs, cfg), nil, nil); err == nil {
		t.Fatal("unbounded enrollments accepted")
	}
	if empty, err := newAdminEmergencyCertificateCatalog(nil, nil, nil); err != nil || len(empty.byID) != 0 {
		t.Fatal("empty emergency configuration enabled a default")
	}
}
