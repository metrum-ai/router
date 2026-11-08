// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/x509"
	"errors"
	"path/filepath"
	"testing"
)

func TestRuntimeMachineCAReloadPublishesCompleteGeneration(t *testing.T) {
	caA, keyA, _ := certificateBindingCA(t)
	caB, keyB, _ := certificateBindingCA(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	trustStoreWriteCA(t, path, caA)
	old, _ := generationKeyFixture(t, "admin:callers:list")
	old.config.Admin.Authentication.MTLS = AdminMTLSTrustConfig{Issuer: "authority", CAFiles: []string{path}}
	trust, err := NewAdminMTLSTrustStore(old.config.Admin.Authentication.MTLS)
	if err != nil {
		t.Fatal(err)
	}
	old.trust = trust
	// The CA source rebuilds from the CURRENT configuration and synchronized
	// authorizer, never from a captured generation.
	svc := &Service{authorizer: old.authorizer, cfg: old.config}
	fail := false
	prepares := 0
	svc.machineGenerationBuilder = func(cfg *Config, compatible *authorizer) (*adminMachineGeneration, error) {
		prepares++
		if fail {
			return nil, errors.New("complete replacement rejected")
		}
		roots, err := NewAdminMTLSTrustStore(cfg.Admin.Authentication.MTLS)
		if err != nil {
			return nil, err
		}
		return &adminMachineGeneration{auth: old.auth, keys: old.keys, trust: roots, authorizer: compatible, config: cfg}, nil
	}
	if err := svc.publishAdminMachineGeneration(old); err != nil {
		t.Fatal(err)
	}
	source := svc.machineClientCASource()
	notifications := 0
	var last *x509.CertPool
	source.OnReload(func(pool *x509.CertPool) { notifications++; last = pool })
	original := source.Snapshot()
	if original == nil || notifications != 1 || last != original {
		t.Fatal("missing initial current generation CA publication")
	}
	trustStoreWriteCA(t, path, caB)
	fail = true
	if err := source.Reload(); err == nil {
		t.Fatal("partial CA reload bypassed failed full generation preparation")
	}
	if svc.machineGeneration.Load() != old || source.Snapshot() != original || notifications != 1 {
		t.Fatal("failed CA preparation disturbed old bundle or notified handshake")
	}
	fail = false
	if err := source.Reload(); err != nil {
		t.Fatal(err)
	}
	next := svc.machineGeneration.Load()
	if next == old || next.authorizer != old.authorizer || next.keys != old.keys || next.trust == old.trust || prepares != 2 || notifications != 2 || last != source.Snapshot() {
		t.Fatal("CA reload did not publish a complete replacement pinned to the synchronized authorizer through the shared publisher")
	}
	if svc.currentAuthorizer() != next.authorizer {
		t.Fatal("CA reload published a generation pinned to authority that the policy synchronization does not hold")
	}
	leafA, leafB := runtimeMTLSLeaf(t, caA, keyA), runtimeMTLSLeaf(t, caB, keyB)
	if _, err := leafA.Verify(x509.VerifyOptions{Roots: source.Snapshot(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("removed root retained by newly published union")
	}
	if _, err := leafB.Verify(x509.VerifyOptions{Roots: source.Snapshot(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal("new root missing from complete union", err)
	}
	// A captured old generation remains immutable, not modified by CA polling.
	if _, err := leafA.Verify(x509.VerifyOptions{Roots: old.trust.Snapshot(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal("reload mutated old generation instead of replacing", err)
	}
}
