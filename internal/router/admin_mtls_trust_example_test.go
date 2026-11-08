// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeMTLSTrustExampleAndInputIsolation(t *testing.T) {
	data, err := os.ReadFile("../../examples/admin-mtls-trust-domains.fragment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fragment struct {
		Admin struct {
			Authentication struct {
				MTLS AdminMTLSTrustConfig `yaml:"mtls"`
			} `yaml:"authentication"`
		} `yaml:"admin"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&fragment); err != nil {
		t.Fatal(err)
	}
	cfg := fragment.Admin.Authentication.MTLS
	if len(cfg.TrustDomains) != 2 || cfg.TrustDomains[0].Issuer == cfg.TrustDomains[1].Issuer || cfg.TrustDomains[0].Subjects[0].SAN != cfg.TrustDomains[1].Subjects[0].SAN || cfg.TrustDomains[0].Subjects[0].Subject == cfg.TrustDomains[1].Subjects[0].Subject {
		t.Fatal("example does not demonstrate scoped shared SAN")
	}
	caA, _, _ := certificateBindingCA(t)
	caB, _, _ := certificateBindingCA(t)
	paths := []string{filepath.Join(t.TempDir(), "a.pem"), filepath.Join(t.TempDir(), "b.pem")}
	trustStoreWriteCA(t, paths[0], caA)
	trustStoreWriteCA(t, paths[1], caB)
	cfg.TrustDomains[0].CAFiles = []string{paths[0]}
	cfg.TrustDomains[1].CAFiles = []string{paths[1]}
	store, err := NewAdminMTLSTrustStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Caller mutations cannot corrupt the immutable publication OR the remembered
	// source paths/mappings used by a subsequent file-only reload.
	cfg.TrustDomains[0].CAFiles[0] = "not-a-real-file"
	cfg.TrustDomains[0].Subjects[0].Subject = "mutated"
	if err := store.Reload(); err != nil {
		t.Fatal("caller mutated retained configuration", err)
	}
	if got := store.current.Load().identity.domains[0].subjects["uri:spiffe://workloads.example/automation"]; got != "automation-a" {
		t.Fatal("caller mutated stored SAN map")
	}
}
