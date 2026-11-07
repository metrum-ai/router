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

func TestAdminEmergencyDeactivationAtomicAndDurable(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	path := filepath.Join(t.TempDir(), "config.sqlite")
	s, db := emergencyActivationFixture(t, path, func() time.Time { return now })
	if err := db.Exec("CREATE TABLE emergency_deactivate_events (activation_id TEXT NOT NULL, reason TEXT NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	cred := adminEmergencyCredential{ID: "rescue-1", Subject: "emergency:rescue", Issuer: "local", Fingerprint: strings.Repeat("c", 64), ExpiresAt: now.Add(30 * time.Minute)}
	active, err := s.Activate(context.Background(), cred, "original incident", now.Add(10*time.Minute), emergencyTestRecorder)
	if err != nil {
		t.Fatal(err)
	}
	record := func(tx *gorm.DB, d adminEmergencyDeactivation) error {
		return tx.Exec("INSERT INTO emergency_deactivate_events (activation_id,reason) VALUES (?,?)", d.Activation.ActivationID, d.Reason).Error
	}
	if _, err := s.Deactivate(context.Background(), cred.ID, "stop", func(tx *gorm.DB, d adminEmergencyDeactivation) error {
		if err := record(tx, d); err != nil {
			return err
		}
		return errors.New("private database details")
	}); !errors.Is(err, errAdminEmergencyPersistence) {
		t.Fatal("audit failure did not deny")
	}
	var count int64
	if err := db.Table("emergency_deactivate_events").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed transaction left an event")
	}
	if _, err := s.Use(context.Background(), cred, emergencyTestRecorder); err != nil {
		t.Fatal("audit rollback lost activation", err)
	}
	redacted := false
	s.redactReason = func(string) string { redacted = true; return "sanitized stop" }
	stopped, err := s.Deactivate(context.Background(), cred.ID, "do-not-log", record)
	if err != nil {
		t.Fatal(err)
	}
	if !redacted || stopped.Reason != "sanitized stop" || stopped.Activation.ActivationID != active.ActivationID || !stopped.DeactivatedAt.Equal(now) {
		t.Fatal("incorrect deactivation metadata")
	}
	raw, err := json.Marshal(stopped)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), cred.Fingerprint) || strings.Contains(string(raw), "do-not-log") {
		t.Fatal("protected material leaked")
	}
	if _, err := s.Use(context.Background(), cred, emergencyTestRecorder); !errors.Is(err, errAdminEmergencyInactive) {
		t.Fatal("explicitly deactivated credential still usable")
	}
	if _, err := s.Deactivate(context.Background(), cred.ID, "again", record); !errors.Is(err, errAdminEmergencyInactive) {
		t.Fatal("repeated deactivation changed history")
	}
	if err := db.Table("emergency_deactivate_events").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("missing/duplicated deactivation event")
	}
	var row adminEmergencyActivationRow
	if err := db.First(&row, "activation_id = ?", active.ActivationID).Error; err != nil || row.DeactivatedAtMS != now.UnixMilli() || row.Reason != "original incident" {
		t.Fatal("history not preserved")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, _ := emergencyActivationFixture(t, path, func() time.Time { return now })
	if _, err := reopened.Use(context.Background(), cred, emergencyTestRecorder); !errors.Is(err, errAdminEmergencyInactive) {
		t.Fatal("closed-pool reopen lost deactivation")
	}
}

func TestAdminEmergencyDeactivationCancellationAndExpiredEnrollment(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	cred := adminEmergencyCredential{ID: "rescue-2", Subject: "emergency:rescue", Issuer: "local", Fingerprint: strings.Repeat("d", 64), ExpiresAt: now.Add(time.Minute)}
	active, err := s.Activate(context.Background(), cred, "incident", now.Add(30*time.Second), emergencyTestRecorder)
	if err != nil {
		t.Fatal(err)
	}
	recorder := func(tx *gorm.DB, d adminEmergencyDeactivation) error { return emergencyTestRecorder(tx, d.Activation) }
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Deactivate(ctx, cred.ID, "stop", func(tx *gorm.DB, d adminEmergencyDeactivation) error {
		if err := recorder(tx, d); err != nil {
			return err
		}
		cancel()
		return nil
	}); err == nil {
		t.Fatal("cancelled transaction committed")
	}
	if _, err := s.Use(context.Background(), cred, emergencyTestRecorder); err != nil {
		t.Fatal("cancel rollback lost state")
	}
	for _, tc := range []struct {
		id, reason string
		record     func(*gorm.DB, adminEmergencyDeactivation) error
	}{{"", "stop", recorder}, {cred.ID, "", recorder}, {cred.ID, strings.Repeat("x", 513), recorder}, {cred.ID, "stop", nil}} {
		if _, err := s.Deactivate(context.Background(), tc.id, tc.reason, tc.record); !errors.Is(err, errAdminEmergencyInvalid) {
			t.Fatal("invalid input accepted")
		}
	}
	// Explicit cleanup remains possible after enrollment/activation expiry and
	// without a currently configured credential descriptor or its verifier.
	now = cred.ExpiresAt.Add(time.Minute)
	if d, err := s.Deactivate(context.Background(), cred.ID, "cleanup expired", recorder); err != nil || d.Activation.ActivationID != active.ActivationID {
		t.Fatal("expired enrollment could not be deactivated", err)
	}
	var count int64
	if err := db.Model(&adminEmergencyActivationRow{}).Where("credential_id = ? AND deactivated_at_ms = 0", cred.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("active history index not cleared")
	}
}
