// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// Command-controller/state integration. Actual binary/listener/Authorize E2E
// is separate; Use here is the durable state check, not a dispatched operation.
func TestAdminEmergencyOfflineCommandLifecycle(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s, db := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	cred := adminEmergencyCredential{ID: "offline-rescue", Subject: "emergency:rescue", Issuer: "local", Fingerprint: strings.Repeat("e", 64), ExpiresAt: now.Add(30 * time.Minute)}
	lookup := func(id string) (adminEmergencyCredential, bool) { return cred, id == cred.ID }
	deactRecord := func(tx *gorm.DB, d adminEmergencyDeactivation) error { return emergencyTestRecorder(tx, d.Activation) }
	command, err := newAdminEmergencyOfflineCommand(s, lookup, emergencyTestRecorder, deactRecord)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"activate", "--credential-id", cred.ID, "--reason", "incident-1", "--until", now.Add(10 * time.Minute).Format(time.RFC3339)}
	if err := command.Run(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	var active adminEmergencyActivation
	if err := json.Unmarshal(out.Bytes(), &active); err != nil || active.CredentialID != cred.ID {
		t.Fatal("invalid command output", err)
	}
	if strings.Contains(out.String(), cred.Fingerprint) {
		t.Fatal("binding leaked")
	}
	if _, err := s.Use(context.Background(), cred, emergencyTestRecorder); err != nil {
		t.Fatal("activated credential inactive", err)
	}
	out.Reset()
	if err := command.Run(context.Background(), []string{"deactivate", "--credential-id", cred.ID, "--reason", "incident closed"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Use(context.Background(), cred, emergencyTestRecorder); !errors.Is(err, errAdminEmergencyInactive) {
		t.Fatal("command deactivation did not deny use")
	}
	var events int64
	if err := db.Table("emergency_test_events").Count(&events).Error; err != nil || events != 3 {
		t.Fatal("lifecycle acknowledgements missing")
	}
}

func TestAdminEmergencyOfflineCommandRejectsInvalidAndFailedAudit(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s, _ := emergencyActivationFixture(t, filepath.Join(t.TempDir(), "config.sqlite"), func() time.Time { return now })
	cred := adminEmergencyCredential{ID: "rescue", Subject: "emergency:rescue", Issuer: "local", Fingerprint: strings.Repeat("f", 64), ExpiresAt: now.Add(30 * time.Minute)}
	fail := func(*gorm.DB, adminEmergencyActivation) error { return errors.New("private database details") }
	command, err := newAdminEmergencyOfflineCommand(s, func(string) (adminEmergencyCredential, bool) { return cred, true }, fail, func(*gorm.DB, adminEmergencyDeactivation) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"activate", "--credential-id", cred.ID, "--reason", "incident", "--until", now.Add(time.Minute).Format(time.RFC3339)}, {"activate", "--credential-id", cred.ID, "--reason", "incident", "--until", "bad"}, {"deactivate", "--credential-id", cred.ID, "--reason", "incident", "--url", "http://remote.invalid"}} {
		var out bytes.Buffer
		if err := command.Run(context.Background(), args, &out); err == nil || strings.Contains(err.Error(), "private database details") || out.Len() != 0 {
			t.Fatal("invalid/failed command acknowledged or leaked")
		}
	}
	if _, err := s.Use(context.Background(), cred, emergencyTestRecorder); !errors.Is(err, errAdminEmergencyInactive) {
		t.Fatal("failed command activated credential")
	}
}
