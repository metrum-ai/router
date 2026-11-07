// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPolicyRuntimeMigrationSeedsLegacyAndPreservesRevision(t *testing.T) {
	s, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "legacy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.db.Migrator().HasTable(&policyMutationRevision{}) {
		t.Fatal("serving store open invented runtime schema")
	}
	if err := s.CreateAuthzPolicySet(authzPolicySetInput{ID: "legacy", Name: "legacy", Version: 1, Policy: []string{"p, root, *, admin:policy, read|write"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateAuthzPolicySet("legacy", "offline", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := loadAuthzPolicyLines(s.db, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPolicyRuntimeMigration(s.db); err != nil {
		t.Fatal(err)
	}
	var row policyMutationRevision
	if err := s.db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Revision != 0 || row.ActiveSetID != "legacy" || row.ConfigRevision != 0 {
		t.Fatalf("bad initial row: %+v", row)
	}
	if err := s.db.Create(&policyMutationRevision{ID: 2}).Error; err == nil {
		t.Fatal("singleton constraint absent")
	}
	if err := s.db.Model(&policyMutationRevision{}).Where("id = ?", 1).Update("revision", -1).Error; err == nil {
		t.Fatal("negative revision constraint absent")
	}
	if err := s.db.Model(&policyMutationRevision{}).Where("id = ?", 1).Update("revision", 9).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyPolicyRuntimeMigration(s.db); err != nil {
		t.Fatal(err)
	}
	s.db.First(&row, 1)
	if row.Revision != 9 {
		t.Fatal("reapply reset monotonic revision")
	}
	after, err := loadAuthzPolicyLines(s.db, "legacy")
	if err != nil || len(before) != len(after) || before[0] != after[0] {
		t.Fatal("migration rewrote legacy policy")
	}
	if err := s.db.Delete(&policyMutationRevision{}, "id = ?", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := applyPolicyRuntimeMigration(s.db); err == nil {
		t.Fatal("reapply silently reset missing revision row")
	}
}
func TestPolicyRuntimeMigrationRefusesAmbiguousLegacyAuthority(t *testing.T) {
	s, err := openTestUsageStorePath(filepath.Join(t.TempDir(), "ambiguous.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"a", "b"} {
		if err := s.db.Create(&authzPolicySetRecord{ID: id, Name: id, Version: 1, Status: authzPolicyStatusActive}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := applyPolicyRuntimeMigration(s.db); err == nil {
		t.Fatal("multiple active policies accepted")
	}
	if s.db.Migrator().HasTable(&policyMutationRevision{}) {
		t.Fatal("ambiguous seed left invented runtime state")
	}
}
