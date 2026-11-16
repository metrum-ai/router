// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPolicyPreviewIsReadOnlyAndDiffIsBounded(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	deps.Build = func(_ *gorm.DB, _ *Config, lines []string) (*Service, error) {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, e
	}
	audit := func(tx *gorm.DB, rev int64, r policyResourceResult) error {
		return insertAuthzPolicyAudit(tx, r.SetID, p.Subject, "change", "req", "", time.Now())
	}
	for i, id := range []string{"base", "target"} {
		candidate := lines
		if i == 1 {
			candidate = append(append([]string{}, lines...), "p, reader, *, admin:reports, read")
		}
		if _, err := s.mutatePolicyResource(WithPrincipal(context.Background(), p), p, deps, policyResourceCommand{Kind: "create-set", SetID: id, Name: id, Version: 1, Policy: candidate, ExpectedRevision: int64(i)}, audit); err != nil {
			t.Fatal(err)
		}
	}
	handlers := newPolicyAdminHandlers(s, deps, audit)
	for _, key := range []string{"policy.set.validate", "policy.set.diff"} {
		r := httptest.NewRequest("GET", "/admin/v1/policy-sets/target/diff?against=base", nil).WithContext(WithPrincipal(context.Background(), p))
		r.SetPathValue("set_id", "target")
		w := httptest.NewRecorder()
		h := handlers[key]
		if h == nil {
			t.Fatalf("missing %s", key)
		}
		h(w, r)
		if w.Code != 200 {
			t.Fatalf("%s=%d %s", key, w.Code, w.Body.String())
		}
		if key == "policy.set.validate" {
			var v struct {
				Valid       bool `json:"valid"`
				CanActivate bool `json:"can_activate"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || !v.Valid || !v.CanActivate {
				t.Fatalf("validate=%s %v", w.Body.String(), err)
			}
		} else {
			var d struct{ Added, Removed []string }
			if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || len(d.Added) != 1 || len(d.Removed) != 0 {
				t.Fatalf("diff=%s %v", w.Body.String(), err)
			}
		}
	}
	var row policyMutationRevision
	s.db.First(&row, 1)
	if row.Revision != 2 {
		t.Fatal("preview advanced revision")
	}
	var count int64
	s.db.Model(&authzPolicyAuditEventRecord{}).Count(&count)
	if count != 2 {
		t.Fatal("preview wrote mutation audit")
	}
}

func TestPolicySimulationUsesAuthenticatedDomainAndHidesPolicyRules(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	lines = append(lines, "p, root, team/prod, admin:reports, read", "p, root, other/prod, admin:reports, read")
	deps.Build = func(_ *gorm.DB, _ *Config, candidateLines []string) (*Service, error) {
		a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: candidateLines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a, authorizationAudit: func(context.Context, Principal, string, string, string, error) error { return nil }}, err
	}
	preflight, preflightErr := deps.Build(s.db, deps.PublishedConfig(), lines)
	if preflightErr != nil {
		t.Fatalf("preflight build: %v", preflightErr)
	}
	if preflight.currentAuthorizer() == nil {
		t.Fatal("preflight authorizer nil")
	}
	audit := func(tx *gorm.DB, _ int64, result policyResourceResult) error {
		return insertAuthzPolicyAudit(tx, result.SetID, p.Subject, "change", "req", "", time.Now())
	}
	if _, err := s.mutatePolicyResource(WithPrincipal(context.Background(), p), p, deps, policyResourceCommand{Kind: "create-set", SetID: "candidate", Name: "candidate", Version: 1, Policy: lines}, audit); err != nil {
		t.Fatal(err)
	}
	handler := newPolicyAdminHandlers(s, deps, audit)["policy.set.simulate"]
	for _, tc := range []struct {
		name, object string
		allowed      bool
	}{{"own domain allowed", "admin:reports", true}, {"ungranted action denied", "admin:reports", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithPrincipal(context.Background(), p)
			ctx = WithAdminAuditMeta(ctx, AdminAuditMeta{Subject: p.Subject, Domain: "team/prod"})
			action := "read"
			if !tc.allowed {
				action = "write"
			}
			r := httptest.NewRequest("POST", "/admin/v1/policy-sets/candidate/simulate", strings.NewReader(`{"object":"`+tc.object+`","action":"`+action+`"}`)).WithContext(ctx)
			r.SetPathValue("set_id", "candidate")
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var got policySimulationDocument
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Allowed != tc.allowed || got.Decision != map[bool]string{true: "allow", false: "deny"}[tc.allowed] {
				t.Fatalf("simulation=%+v", got)
			}
			if strings.Contains(w.Body.String(), "other/prod") {
				t.Fatalf("response disclosed candidate rules: %s", w.Body.String())
			}
			if tc.allowed && !strings.Contains(got.Explanation, "deciding policy rule") {
				t.Fatalf("simulation explanation omits deciding rule: %+v", got)
			}
			if strings.Contains(got.Explanation, "root") || strings.Contains(got.Explanation, "domain") || strings.Contains(got.Explanation, "admin:reports") {
				t.Fatalf("simulation explanation disclosed policy fields: %+v", got)
			}
		})
	}
	r := httptest.NewRequest("POST", "/admin/v1/policy-sets/candidate/simulate", strings.NewReader(`{"object":"admin:reports","action":"read"}`)).WithContext(WithPrincipal(context.Background(), p))
	r.SetPathValue("set_id", "candidate")
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != 403 {
		t.Fatalf("missing authenticated domain status=%d body=%s", w.Code, w.Body.String())
	}
	ctx, cancel := context.WithCancel(WithPrincipal(context.Background(), p))
	ctx = WithAdminAuditMeta(ctx, AdminAuditMeta{Subject: p.Subject, Domain: "team/prod"})
	cancel()
	r = httptest.NewRequest("POST", "/admin/v1/policy-sets/candidate/simulate", strings.NewReader(`{"object":"admin:reports","action":"read"}`)).WithContext(ctx)
	r.SetPathValue("set_id", "candidate")
	w = httptest.NewRecorder()
	handler(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), "context canceled") {
		t.Fatalf("canceled simulation status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/admin/v1/policy-sets/missing/simulate", strings.NewReader(`{"object":"admin:reports","action":"read"}`)).WithContext(WithAdminAuditMeta(WithPrincipal(context.Background(), p), AdminAuditMeta{Subject: p.Subject, Domain: "team/prod"}))
	r.SetPathValue("set_id", "missing")
	w = httptest.NewRecorder()
	handler(w, r)
	if w.Code != 404 {
		t.Fatalf("missing candidate status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPolicySimulationUsesAdminKeyCompatibilityIdentity(t *testing.T) {
	s, deps, actor, _ := policyMutationFixture(t)
	p := Principal{Subject: "adminkey:shared", Kind: "adminkey", CredentialID: "key-42"}
	legacy := AdminAuthConfig{APIKeys: []AdminAPIKeyConfig{{KeyID: "key-42", Subject: p.Subject, Domain: AdminGlobalDomain, Permissions: []string{"config:read"}}}}
	lines := []string{"p, root, *, admin:policy, read|write|activate", "g, root, security-admin, *"}
	deps.Build = func(_ *gorm.DB, _ *Config, candidateLines []string) (*Service, error) {
		a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: candidateLines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, err
	}
	audit := func(tx *gorm.DB, _ int64, result policyResourceResult) error {
		return insertAuthzPolicyAudit(tx, result.SetID, actor.Subject, "change", "req-map", "", time.Now())
	}
	if _, err := s.mutatePolicyResource(WithPrincipal(context.Background(), actor), actor, deps, policyResourceCommand{Kind: "create-set", SetID: "mapped", Name: "mapped", Version: 1, Policy: lines}, audit); err != nil {
		t.Fatal(err)
	}
	deps.Build = func(_ *gorm.DB, _ *Config, candidateLines []string) (*Service, error) {
		a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: candidateLines}, nil, nil, legacy)
		if err == nil {
			err = addGlobalCompatibilityGrants(a, nil, legacy)
		}
		return &Service{authorizer: a}, err
	}
	ctx := WithAdminAuditMeta(WithPrincipal(context.Background(), p), AdminAuditMeta{Subject: p.Subject, Domain: AdminGlobalDomain})
	request := httptest.NewRequest("POST", "/admin/v1/policy-sets/mapped/simulate", strings.NewReader(`{"object":"admin:config","action":"read"}`)).WithContext(ctx)
	request.SetPathValue("set_id", "mapped")
	response := httptest.NewRecorder()
	newPolicyAdminHandlers(s, deps, audit)["policy.set.simulate"].ServeHTTP(response, request)
	var got policySimulationDocument
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || !got.Allowed {
		t.Fatalf("mapped simulation status=%d body=%s result=%+v", response.Code, response.Body.String(), got)
	}
	for _, tc := range []struct {
		name      string
		principal Principal
		object    string
	}{
		{name: "invalid principal", principal: Principal{Kind: "adminkey", CredentialID: "key-42"}, object: "admin:config"},
		{name: "internal object", principal: p, object: internalAuthorizationSubjectPrefix + "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithAdminAuditMeta(WithPrincipal(context.Background(), tc.principal), AdminAuditMeta{Subject: tc.principal.Subject, Domain: AdminGlobalDomain})
			r := httptest.NewRequest("POST", "/admin/v1/policy-sets/mapped/simulate", strings.NewReader(`{"object":"`+tc.object+`","action":"read"}`)).WithContext(ctx)
			r.SetPathValue("set_id", "mapped")
			w := httptest.NewRecorder()
			newPolicyAdminHandlers(s, deps, audit)["policy.set.simulate"].ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("invalid simulation status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	candidate, err := deps.Build(s.db, deps.PublishedConfig(), lines)
	if err != nil {
		t.Fatal(err)
	}
	if err := candidate.Authorize(WithPrincipal(context.Background(), p), p, AdminGlobalDomain, "admin:config", "read"); err != nil {
		t.Fatalf("same mapped policy denied enforcement: %v", err)
	}
	if candidate.currentAuthorizer().enforce(authorizationSubject{subject: p.Subject, domain: AdminGlobalDomain, source: p.Kind}, "admin:config", "read") {
		t.Fatal("test policy unexpectedly allows raw admin-key principal")
	}
}
