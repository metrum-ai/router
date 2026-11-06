// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func emergencyActivationFixture(t *testing.T, path string, clock func() time.Time) (*adminEmergencyActivationStore, *gorm.DB) {
	t.Helper()
	r, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	if !r.db.Migrator().HasTable(configControlPlaneShadowRunsTable) {
		applyConfigControlPlaneMigrationsForTest(t, r)
	} else if status, err := r.Verify(); err != nil || !status.Compatible || status.State != "current" {
		t.Fatalf("reopened migration ledger is not current: status=%+v err=%v", status, err)
	}
	// Apply only this store's DDL here. Phase-12 manifest registration follows
	// phase 11 in the integration slice; this does not claim ledger coverage.
	if err := r.db.Transaction(applyAdminEmergencyActivationSchema); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec("CREATE TABLE IF NOT EXISTS emergency_test_events (activation_id TEXT NOT NULL, credential_id TEXT NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	s, err := newAdminEmergencyActivationStore(r.db, 1, clock, func(reason string) string {
		return sanitizeInternalDiagnosticText(reason, 512)
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, r.db
}

func emergencyTestRecorder(tx *gorm.DB, a adminEmergencyActivation) error {
	return tx.Exec("INSERT INTO emergency_test_events (activation_id, credential_id) VALUES (?, ?)", a.ActivationID, a.CredentialID).Error
}

func TestAdminEmergencyActivationDurableAtomicAndBounded(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "config.sqlite")
	s, db := emergencyActivationFixture(t, path, clock)
	cred := adminEmergencyCredential{ID: "rescue-1", Subject: "emergency:rescue", Issuer: "local", Fingerprint: strings.Repeat("a", 64), ExpiresAt: now.Add(30 * time.Minute)}
	ctx := context.Background()
	if _, err := s.Use(ctx, cred, emergencyTestRecorder); err == nil {
		t.Fatal("inactive credential accepted")
	}
	if _, err := s.Activate(ctx, cred, "incident-123", now.Add(10*time.Minute), nil); err == nil {
		t.Fatal("activation without durable event writer accepted")
	}
	active, err := s.Activate(ctx, cred, "incident-123", now.Add(10*time.Minute), emergencyTestRecorder)
	if err != nil {
		t.Fatal(err)
	}
	if active.ActivationID == "" || active.Reason != "incident-123" {
		t.Fatalf("invalid safe metadata: %#v", active)
	}
	raw, err := json.Marshal(active)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), cred.Fingerprint) {
		t.Fatal("credential binding leaked in metadata")
	}
	if _, err := s.Use(ctx, cred, nil); err == nil {
		t.Fatal("use without acknowledged event accepted")
	}
	var before, after int64
	if err := db.Table("emergency_test_events").Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	_, err = s.Activate(ctx, cred, "incident-456", now.Add(5*time.Minute), func(tx *gorm.DB, a adminEmergencyActivation) error {
		if err := emergencyTestRecorder(tx, a); err != nil {
			return err
		}
		return errors.New("injected event failure")
	})
	if err == nil {
		t.Fatal("failed event did not abort activation")
	}
	if err := db.Table("emergency_test_events").Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("event from rejected transaction persisted")
	}
	used, err := s.Use(ctx, cred, emergencyTestRecorder)
	if err != nil || used.ActivationID != active.ActivationID {
		t.Fatalf("rollback lost previous activation: %v", err)
	}
	if err := db.Table("emergency_test_events").Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Use(ctx, cred, func(tx *gorm.DB, a adminEmergencyActivation) error {
		if err := emergencyTestRecorder(tx, a); err != nil {
			return err
		}
		return errors.New("injected use event failure")
	}); err == nil {
		t.Fatal("use accepted without durable event")
	}
	if err := db.Table("emergency_test_events").Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("rejected use left a success event")
	}
	changed := cred
	changed.Fingerprint = strings.Repeat("b", 64)
	if _, err := s.Use(ctx, changed, emergencyTestRecorder); err == nil {
		t.Fatal("replacement credential inherited previous activation")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Use(cancelled, cred, emergencyTestRecorder); err == nil {
		t.Fatal("cancelled use accepted")
	}
	// A new DB handle/store must recover durable state, not process-local state.
	reopened, _ := emergencyActivationFixture(t, path, clock)
	if got, err := reopened.Use(ctx, cred, emergencyTestRecorder); err != nil || got.ActivationID != active.ActivationID {
		t.Fatalf("durable activation not recovered: %v", err)
	}
	if err := db.Table("emergency_test_events").Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Use(ctx, cred, func(tx *gorm.DB, a adminEmergencyActivation) error {
		if err := emergencyTestRecorder(tx, a); err != nil {
			return err
		}
		now = active.Until
		return nil
	}); err == nil {
		t.Fatal("activation expired during event write was accepted")
	}
	if err := db.Table("emergency_test_events").Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("expired use left a success event")
	}
	if _, err := reopened.Use(ctx, cred, emergencyTestRecorder); err == nil {
		t.Fatal("activation accepted at its exact deadline")
	}
}

func TestAdminEmergencyActivationRejectsUnsafeWindowsAndReplicas(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	cred := adminEmergencyCredential{ID: "rescue-1", Subject: "emergency:rescue", Issuer: "local", Fingerprint: strings.Repeat("a", 64), ExpiresAt: now.Add(30 * time.Minute)}
	for _, tc := range []struct {
		name, reason  string
		until, expiry time.Time
	}{
		{"no-reason", " ", now.Add(time.Minute), cred.ExpiresAt},
		{"deadline-in-past", "incident", now, cred.ExpiresAt},
		{"past-credential-expiry", "incident", now.Add(31 * time.Minute), cred.ExpiresAt},
		{"expired-credential", "incident", now.Add(time.Minute), now},
		{"overlong-credential", "incident", now.Add(time.Minute), now.Add(2 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cred
			c.ExpiresAt = tc.expiry
			if _, err := s.Activate(context.Background(), c, tc.reason, tc.until, emergencyTestRecorder); err == nil {
				t.Fatal("unsafe activation accepted")
			}
		})
	}
	if _, err := newAdminEmergencyActivationStore(db, 2, func() time.Time { return now }, func(s string) string { return sanitizeInternalDiagnosticText(s, 512) }); err == nil {
		t.Fatal("SQLite multi-replica emergency store accepted")
	}
	if _, err := newAdminEmergencyActivationStore(db, 1, func() time.Time { return now }, nil); err == nil {
		t.Fatal("missing shared redactor accepted")
	}
	active, err := s.Activate(context.Background(), cred, "rtr_admin_fixture_sensitive", now.Add(time.Minute), emergencyTestRecorder)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(active.Reason, "rtr_admin_fixture_sensitive") {
		t.Fatal("credential-shaped reason was retained")
	}
}
