// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPolicyResourceHTTPReadsHonorCancellation(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	deps.Build = func(_ *gorm.DB, _ *Config, lines []string) (*Service, error) {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: lines}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, e
	}
	audit := func(tx *gorm.DB, rev int64, r policyResourceResult) error {
		return insertAuthzPolicyAudit(tx, r.SetID, p.Subject, "change", "req", "", time.Now())
	}
	if _, err := s.mutatePolicyResource(WithPrincipal(context.Background(), p), p, deps, policyResourceCommand{Kind: "create-set", SetID: "one", Name: "one", Version: 1, Policy: lines}, audit); err != nil {
		t.Fatal(err)
	}
	handlers := newPolicyAdminHandlers(s, deps, audit)
	for _, key := range []string{"policy.set.get", "policy.rules.list", "policy.memberships.list"} {
		t.Run(key, func(t *testing.T) {
			ctx, cancel := context.WithCancel(WithPrincipal(context.Background(), p))
			cancel()
			r := httptest.NewRequest("GET", "/admin/v1/policy-sets/one", nil).WithContext(ctx)
			r.SetPathValue("set_id", "one")
			w := httptest.NewRecorder()
			if handlers[key] == nil {
				t.Fatalf("missing handler %s", key)
			}
			handlers[key](w, r)
			if w.Code != 503 {
				t.Fatalf("canceled read status=%d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "context canceled") {
				t.Fatal("private cancellation diagnostic escaped")
			}
		})
	}
}
