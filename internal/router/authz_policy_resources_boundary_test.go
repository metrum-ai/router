// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"testing"
)

func TestPolicyResourceUnauthorizedCannotProbeOrBuild(t *testing.T) {
	s, deps, _, _ := policyMutationFixture(t)
	deps.Build = func(*gorm.DB, *Config, []string) (*Service, error) {
		t.Fatal("unauthorized request built candidate")
		return nil, nil
	}
	if err := s.db.Create(&authzPolicySetRecord{ID: "immutable", Name: "immutable", Version: 1, Status: authzPolicyStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	p := Principal{Subject: "outsider", Kind: "mtls"}
	for _, cmd := range []policyResourceCommand{
		{Kind: "replace-set", SetID: "absent"},
		{Kind: "delete-set", SetID: "immutable"},
		{Kind: "create-set", SetID: "new", Policy: make([]string, maxPolicyGuardCandidateRows+1)},
		{Kind: "create-rule", SetID: "immutable", Fields: []string{"invalid"}},
		{Kind: "delete-set", SetID: "immutable", ExpectedRevision: 999},
		{Kind: "create-set", SetID: ""},
	} {
		_, err := s.mutatePolicyResource(WithPrincipal(context.Background(), p), p, deps, cmd, func(*gorm.DB, int64, policyResourceResult) error { t.Fatal("unauthorized audit"); return nil })
		if !errors.Is(err, ErrAdminForbidden) {
			t.Errorf("%s/%s unauthorized outcome=%v", cmd.Kind, cmd.SetID, err)
		}
	}
}
