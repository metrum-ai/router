// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"
)

// TestAuthzObjectMatchIsExact proves that a grant for one admin object never
// authorizes a different admin object, across domains and roles. Regression
// coverage for the matcher change from keyMatch2 to exact object equality.
func TestAuthzObjectMatchIsExact(t *testing.T) {
	cases := []struct {
		name   string
		sub    string
		policy []string
		dom    string
		obj    string
		act    string
		want   bool
	}{
		{"A1 scoped reports grant reads reports in own domain", "basic:admin", []string{"p, basic:admin, example-project/dev, admin:reports, read|export|drilldown"}, "example-project/dev", "admin:reports", "read", true},
		{"A2 scoped reports grant must NOT read security_reports", "basic:admin", []string{"p, basic:admin, example-project/dev, admin:reports, read|export|drilldown"}, "example-project/dev", "admin:security_reports", "read", false},
		{"A3 scoped reports grant must NOT list callers", "basic:admin", []string{"p, basic:admin, example-project/dev, admin:reports, read|export|drilldown|list"}, "example-project/dev", "admin:callers", "list", false},
		{"B1 global security grant reads global security", "basic:admin", []string{"p, basic:admin, *, admin:security_reports, read|export"}, "*", "admin:security_reports", "read", true},
		{"B2 global security grant must NOT read global reports", "basic:admin", []string{"p, basic:admin, *, admin:security_reports, read|export"}, "*", "admin:reports", "read", false},
		{"B3 global security grant must NOT read reports in any domain", "basic:admin", []string{"p, basic:admin, *, admin:security_reports, read|export"}, "other-project/prod", "admin:reports", "read", false},
		{"C1 callers grant must NOT read reports", "basic:ops", []string{"p, basic:ops, *, admin:callers, list|issue|rotate|revoke|read"}, "*", "admin:reports", "read", false},
		{"D1 metrics stays distinct", "basic:admin", []string{"p, basic:admin, *, admin:reports, read"}, "*", "metrics", "read", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Source: "static", Policy: c.policy}, nil, nil)
			if err != nil {
				t.Fatalf("newAuthorizer: %v", err)
			}
			if got := a.enforce(authorizationSubject{subject: c.sub, domain: c.dom}, c.obj, c.act); got != c.want {
				t.Errorf("enforce(%s, %s, %s, %s) = %v, want %v", c.sub, c.dom, c.obj, c.act, got, c.want)
			}
		})
	}
}

// TestAuthzActionMatchIsAnchored proves that policy action patterns are
// anchored: a policy action "read" must not match a requested action that
// merely contains "read".
func TestAuthzActionMatchIsAnchored(t *testing.T) {
	policy := []string{"p, basic:admin, *, admin:reports, read"}
	a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Source: "static", Policy: policy}, nil, nil)
	if err != nil {
		t.Fatalf("newAuthorizer: %v", err)
	}
	subject := authorizationSubject{subject: "basic:admin", domain: "*"}
	if !a.enforce(subject, "admin:reports", "read") {
		t.Error("anchored match: read must match read")
	}
	for _, act := range []string{"xreadx", "reads", "readx", "xread"} {
		if a.enforce(subject, "admin:reports", act) {
			t.Errorf("anchored match: policy action read must not match %q", act)
		}
	}
}

// TestAuthzObjectWildcardSemantics pins the supported wildcard behavior: the
// single wildcard "*" matches every object, while partial wildcards are
// rejected at policy load time.
func TestAuthzObjectWildcardSemantics(t *testing.T) {
	a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Source: "static", Policy: []string{"p, basic:admin, example/prod, *, read"}}, nil, nil)
	if err != nil {
		t.Fatalf("newAuthorizer: %v", err)
	}
	subject := authorizationSubject{subject: "basic:admin", domain: "example/prod"}
	if !a.enforce(subject, "admin:reports", "read") {
		t.Error("single wildcard object grant must match admin:reports")
	}
	if !a.enforce(subject, "metrics", "read") {
		t.Error("single wildcard object grant must match metrics")
	}

	if _, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Source: "static", Policy: []string{"p, basic:admin, example/prod, admin:*, read"}}, nil, nil); err == nil {
		t.Error("partial wildcard object admin:* must be rejected at load time")
	}
}
