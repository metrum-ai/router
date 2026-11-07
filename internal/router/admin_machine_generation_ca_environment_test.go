// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"path/filepath"
	"testing"
)

// Policy compatibility test through the sole Service.Authorize, not native
// certificate-authentication evidence. Context capture is an explicit fixture.
func TestRuntimeMachineCAReloadPreservesCallerEnvironmentGrants(t *testing.T) {
	generation, _ := generationKeyFixture(t, "admin:callers:list")
	caller := CallerConfig{ID: "alice", Project: "search", Environment: "dev", MetricsAdmin: true, ContentAdmin: true}
	generation.config.Callers = []CallerConfig{caller}
	callers := map[string]*callerRuntime{"alice": {cfg: caller, project: "search"}}
	compatible, err := newAuthorizer(generation.config.Server.AdminAuth.Authorization, callers, nil, generation.config.Server.AdminAuth)
	if err != nil {
		t.Fatal(err)
	}
	generation.authorizer = compatible
	ca, _, _ := certificateBindingCA(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	trustStoreWriteCA(t, path, ca)
	generation.config.Admin.Authentication.MTLS = AdminMTLSTrustConfig{Issuer: "authority", CAFiles: []string{path}}
	roots, err := NewAdminMTLSTrustStore(generation.config.Admin.Authentication.MTLS)
	if err != nil {
		t.Fatal(err)
	}
	generation.trust = roots
	svc := &Service{quota: &quotaStore{callers: callers}}
	svc.machineGenerationBuilder = func(cfg *Config, authorizer *authorizer) (*adminMachineGeneration, error) {
		roots, err := NewAdminMTLSTrustStore(cfg.Admin.Authentication.MTLS)
		if err != nil {
			return nil, err
		}
		return &adminMachineGeneration{config: cfg, auth: generation.auth, keys: generation.keys, trust: roots, authorizer: authorizer}, nil
	}
	if err := svc.publishAdminMachineGeneration(generation); err != nil {
		t.Fatal(err)
	}
	p := Principal{Subject: "caller:alice", Kind: "mtls", CredentialID: "test-leaf", Issuer: "authority"}
	check := func(g *adminMachineGeneration, domain, object, action string) error {
		ctx := WithPrincipal(context.Background(), p)
		ctx = context.WithValue(ctx, adminMachineGenerationContextKey{}, capturedAdminMachineGeneration{owner: svc, generation: g})
		return svc.Authorize(svc.PinAuthorization(ctx), p, domain, object, action)
	}
	for _, phase := range []string{"before", "after"} {
		if phase == "after" {
			if err := svc.machineClientCASource().Reload(); err != nil {
				t.Fatal(err)
			}
		}
		captured := svc.machineGeneration.Load()
		for _, operation := range []struct{ object, action string }{{authzObjectMetrics, authzActionRead}, {authzObjectContentCapture, authzActionDelete}} {
			if err := check(captured, "search/dev", operation.object, operation.action); err != nil {
				t.Errorf("%s CA reload lost environment-scoped %s grant: %v", phase, operation.object, err)
			}
			if err := check(captured, "search", operation.object, operation.action); err == nil {
				t.Errorf("%s CA reload broadened %s grant to project-only domain", phase, operation.object)
			}
		}
	}
}
