// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
)

func TestExplicitGlobalCallerKeyRetainsRowScope(t *testing.T) {
	cfg := AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{
		{KeyID: "global", Domain: AdminGlobalDomain, Permissions: []string{"admin:callers:rotate", "config:read"}},
		{KeyID: "project", Domain: "team/prod", Permissions: []string{"admin:callers:rotate"}},
	}}
	a, err := newAuthorizer(AdminAuthorizationConfig{}, nil, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{authorizer: a}
	for _, test := range []struct {
		id, domain string
		allow      bool
	}{
		{"global", "team/prod", true}, {"global", "other/prod", true},
		{"project", "team/prod", true}, {"project", "other/prod", false},
	} {
		p := Principal{Subject: "adminkey:" + test.id, Kind: "adminkey", CredentialID: test.id}
		err := svc.Authorize(context.Background(), p, test.domain, "admin:callers", "rotate")
		if (err == nil) != test.allow {
			t.Errorf("%+v: %v", test, err)
		}
		if err := svc.Authorize(context.Background(), p, test.domain, "admin:config", "write"); !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("caller credential acquired unrelated capability: %v", err)
		}
		if err := svc.Authorize(context.Background(), p, test.domain, "admin:config", "read"); !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("global config target widened into a project: %v", err)
		}
	}
	global := Principal{Subject: "adminkey:global", Kind: "adminkey", CredentialID: "global"}
	if err := svc.Authorize(context.Background(), global, AdminGlobalDomain, "admin:config", "read"); err != nil {
		t.Fatal(err)
	}
}
