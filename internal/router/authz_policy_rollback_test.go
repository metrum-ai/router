// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPolicyRollbackClonesHistoryAndAuditFailureAborts(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	lines[0] = strings.Replace(lines[0], "read|write|activate", "read|write|activate|rollback", 1)
	build := func(policy []string) (*Service, error) {
		a, e := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: policy}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, e
	}
	deps.Current = func(*gorm.DB, *Config) (*Service, error) { return build(lines) }
	deps.Build = func(_ *gorm.DB, _ *Config, policy []string) (*Service, error) { return build(policy) }
	audit := func(tx *gorm.DB, rev int64, r policyResourceResult) error {
		return insertAuthzPolicyAudit(tx, r.SetID, p.Subject, "change", "req", "", time.Now())
	}
	ctx := WithPrincipal(context.Background(), p)
	for i, id := range []string{"first", "second"} {
		policy := lines
		if i == 0 {
			policy = append(append([]string{}, lines...), "p, reader, *, admin:reports, read")
		}
		_, err := s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "create-set", SetID: id, Name: "series", Version: i + 1, Policy: policy, ExpectedRevision: int64(i * 2)}, audit)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "activate", SetID: id, ExpectedRevision: int64(i*2 + 1)}, audit)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.getPolicyResource("first")
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "rollback", SetID: "first", ExpectedRevision: 4}, audit)
	if err != nil {
		t.Fatalf("rollback clone=%v", err)
	}
	if rolled.SetID == "first" || rolled.SetID == "second" || rolled.SetID == "" || rolled.Revision != 5 {
		t.Fatalf("bad clone result: %+v", rolled)
	}
	clone, err := s.getPolicyResource(rolled.SetID)
	if err != nil {
		t.Fatal(err)
	}
	if clone.Set.Version != 3 || clone.Set.Status != authzPolicyStatusActive || !reflect.DeepEqual(clone.lines(), before.lines()) {
		t.Fatalf("clone content/version/status: %+v", clone)
	}
	after, err := s.getPolicyResource("first")
	if err != nil {
		t.Fatal(err)
	}
	if before.Set != after.Set || !reflect.DeepEqual(before.Rules, after.Rules) || !reflect.DeepEqual(before.Memberships, after.Memberships) {
		t.Fatal("historical row or child identities changed")
	}
	var runtime policyMutationRevision
	if err := s.db.First(&runtime, 1).Error; err != nil {
		t.Fatal(err)
	}
	if runtime.ActiveSetID != rolled.SetID {
		t.Fatalf("runtime active=%s", runtime.ActiveSetID)
	}
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "rollback", SetID: "second", ExpectedRevision: 5}, func(*gorm.DB, int64, policyResourceResult) error { return errors.New("audit failure") })
	if !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("audit failure=%v", err)
	}
	var count int64
	s.db.Model(&authzPolicySetRecord{}).Count(&count)
	if count != 3 {
		t.Fatalf("failed rollback left clone: sets=%d", count)
	}
	s.db.First(&runtime, 1)
	if runtime.Revision != 5 || runtime.ActiveSetID != rolled.SetID {
		t.Fatal("failed rollback changed runtime")
	}
}
