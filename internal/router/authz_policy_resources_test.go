// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestPolicyResourceCRUDUsesWholeCandidateAndImmutableHistory(t *testing.T) {
	s, deps, p, lines := policyMutationFixture(t)
	deps.Build = func(_ *gorm.DB, _ *Config, policy []string) (*Service, error) {
		a, err := newAuthorizer(AdminAuthorizationConfig{Enabled: true, Policy: policy}, nil, nil, AdminAuthConfig{})
		return &Service{authorizer: a}, err
	}
	ctx := WithPrincipal(context.Background(), p)
	audit := func(tx *gorm.DB, revision int64, _ policyResourceResult) error {
		return insertAuthzPolicyAudit(tx, "draft", p.Subject, "change", "req", "", time.Unix(2, 0).UTC())
	}
	result, err := s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "create-set", SetID: "draft", Name: "root-policy", Version: 1, Policy: lines, ExpectedRevision: 0}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 1 {
		t.Fatalf("revision=%d", result.Revision)
	}
	view, err := s.getPolicyResource("draft")
	if err != nil {
		t.Fatal(err)
	}
	if view.Set.Status != "draft" || len(view.Rules) != 2 || len(view.Memberships) != 1 {
		t.Fatalf("bad created view: %+v", view)
	}
	result, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "create-rule", SetID: "draft", Fields: []string{"p", "reader", "*", "admin:reports", "read"}, ExpectedRevision: 1}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordID == 0 {
		t.Fatal("rule lacks stable ID")
	}
	ruleID := result.RecordID
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "replace-rule", SetID: "draft", RecordID: ruleID, Fields: []string{"p", "reader", "*", "admin:reports", "write"}, ExpectedRevision: 2}, audit)
	if !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("broader replacement=%v", err)
	}
	view, err = s.getPolicyResource("draft")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range view.Rules {
		if r.ID == ruleID && r.Action != "read" {
			t.Fatal("rejected rule replacement changed data")
		}
	}
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "delete-membership", SetID: "draft", RecordID: view.Memberships[0].ID, ExpectedRevision: 2}, audit)
	if !errors.Is(err, ErrLastPolicyAdmin) {
		t.Fatalf("whole-set last admin=%v", err)
	}
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "delete-rule", SetID: "draft", RecordID: ruleID, ExpectedRevision: 2}, audit)
	if err != nil {
		t.Fatal(err)
	}
	membership, err := s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "create-membership", SetID: "draft", Fields: []string{"g", "backup", "security-admin", "*"}, ExpectedRevision: 3}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if membership.RecordID == 0 {
		t.Fatal("membership lacks stable ID")
	}
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "replace-membership", SetID: "draft", RecordID: membership.RecordID, Fields: []string{"g", "backup-two", "security-admin", "*"}, ExpectedRevision: 4}, audit)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "delete-membership", SetID: "draft", RecordID: membership.RecordID, ExpectedRevision: 5}, audit)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "activate", SetID: "draft", ExpectedRevision: 6}, audit)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "replace-set", SetID: "draft", Policy: lines, ExpectedRevision: 7}, audit)
	if !errors.Is(err, errPolicySetImmutable) {
		t.Fatalf("active replacement=%v", err)
	}
	_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "delete-set", SetID: "draft", ExpectedRevision: 7}, audit)
	if !errors.Is(err, errPolicySetImmutable) {
		t.Fatalf("active delete=%v", err)
	}
	for _, status := range []string{authzPolicyStatusActive, authzPolicyStatusRetired} {
		if err := s.db.Model(&authzPolicySetRecord{}).Where("id = ?", "draft").Update("status", status).Error; err != nil {
			t.Fatal(err)
		}
		_, err = s.mutatePolicyResource(ctx, p, deps, policyResourceCommand{Kind: "activate", SetID: "draft", ExpectedRevision: 7}, audit)
		if !errors.Is(err, errPolicySetImmutable) {
			t.Fatalf("%s reactivation=%v", status, err)
		}
	}
}
