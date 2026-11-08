// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"gorm.io/gorm"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPolicyRecordHandlersRespectParentAndWholeSet(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	deps.Build = func(_ *gorm.DB, _ *Config, lines []string) (*Service, error) {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, e
	}
	audit := func(tx *gorm.DB, rev int64, r policyResourceResult) error {
		return insertAuthzPolicyAudit(tx, r.SetID, p.Subject, "change", "req", "", time.Now())
	}
	for i, id := range []string{"one", "two"} {
		_, err := s.mutatePolicyResource(WithPrincipal(context.Background(), p), p, deps, policyResourceCommand{Kind: "create-set", SetID: id, Name: id, Version: 1, Policy: lines, ExpectedRevision: int64(i)}, audit)
		if err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.getPolicyResource("one")
	if err != nil {
		t.Fatal(err)
	}
	handlers := newPolicyAdminHandlers(s, deps, audit)
	request := func(key, method, parent, id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", strings.NewReader(body)).WithContext(WithPrincipal(context.Background(), p))
		r.SetPathValue("set_id", parent)
		r.SetPathValue("record_id", id)
		r.Header.Set("If-Match", "2")
		w := httptest.NewRecorder()
		h := handlers[key]
		if h == nil {
			t.Fatalf("missing %s", key)
		}
		h(w, r)
		return w
	}
	if w := request("policy.rules.list", "GET", "one", "", ""); w.Code != 200 {
		t.Fatalf("rule list=%d %s", w.Code, w.Body.String())
	}
	if w := request("policy.memberships.list", "GET", "one", "", ""); w.Code != 200 {
		t.Fatalf("membership list=%d %s", w.Code, w.Body.String())
	}
	if w := request("policy.rules.create", "POST", "one", "", `{"subject_or_role":"reader","domain":"*","object":"admin:reports","action":"write"}`); w.Code != 403 || !strings.Contains(w.Body.String(), "privilege-escalation") {
		t.Fatalf("whole-set escalation=%d %s", w.Code, w.Body.String())
	}
	other, err := s.getPolicyResource("two")
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatUint(uint64(other.Rules[0].ID), 10)
	if w := request("policy.rules.get", "GET", "one", id, ""); w.Code != 404 {
		t.Fatalf("cross-parent read=%d %s", w.Code, w.Body.String())
	}
	if w := request("policy.rules.delete", "DELETE", "one", id, ""); w.Code != 404 {
		t.Fatalf("cross-parent delete=%d %s", w.Code, w.Body.String())
	}
	root := strconv.FormatUint(uint64(view.Memberships[0].ID), 10)
	if w := request("policy.memberships.delete", "DELETE", "one", root, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "last-policy-admin") {
		t.Fatalf("last admin=%d %s", w.Code, w.Body.String())
	}
}
