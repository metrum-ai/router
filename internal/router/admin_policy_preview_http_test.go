// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"net/http/httptest"
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
