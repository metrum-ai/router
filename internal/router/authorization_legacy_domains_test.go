// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

func TestLegacyPartialDomainNamesRemainLiteral(t *testing.T) {
	cfg := AdminAuthorizationConfig{Enabled: true, Policy: []string{"g, subject, role, project/*", "p, role, project/*, metrics, read"}}
	a, err := newAuthorizer(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"project/*", "project/prod", "other/prod", "*"} {
		if got, want := a.enforce(authorizationSubject{subject: "subject", domain: domain}, "metrics", "read"), domain == "project/*"; got != want {
			t.Errorf("domain %q got %v want %v", domain, got, want)
		}
	}
}
