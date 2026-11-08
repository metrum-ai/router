// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// End-to-end offline command: real SQLite control-plane tables, real enrollment
// descriptors, activate -> durable activation + credential event -> deactivate.
func TestRouterctlEmergencyCommandLifecycle(t *testing.T) {
	db, err := gorm.Open(sqliteDriver.Open(filepath.Join(t.TempDir(), "cp.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyConfigControlPlanePhase11(db); err != nil {
		t.Fatal(err)
	}
	if err := applyAdminEmergencyActivationSchema(db); err != nil {
		t.Fatal(err)
	}
	key := generateTestAdminKey(t, "emergency-offline")
	t.Setenv("TEST_EMERGENCY_OFFLINE_HASH", key.TokenHash)
	now := time.Now().UTC()
	cfg := &Config{}
	cfg.Admin.Enabled = true
	cfg.Admin.Authentication.Emergency.Credentials = []AdminMachineEmergencyCredentialConfig{{
		ID: "emergency-offline", Subject: "emergency:ops", Issuer: "offline", Kind: "api_key",
		KeyHashEnv: "TEST_EMERGENCY_OFFLINE_HASH",
		IssuedAt:   now.Add(-time.Minute).Format(time.RFC3339Nano),
		ExpiresAt:  now.Add(30 * time.Minute).Format(time.RFC3339Nano),
	}}
	if _, err := newAdminEmergencyKeyCatalog(nil, map[string]bool{"emergency-offline": true}, make(chan struct{}, 1), time.Now); err != nil {
		t.Fatal(err)
	}
	run, err := RouterctlEmergencyCommand(db, cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	deadline := now.Add(10 * time.Minute).Format(time.RFC3339Nano)
	if err := run(context.Background(), []string{"activate", "--credential-id", "emergency-offline", "--reason", "vendor outage", "--until", deadline}, &out); err != nil {
		t.Fatalf("activate: %v", err)
	}
	var activated int64
	if err := db.Raw("SELECT COUNT(*) FROM "+adminEmergencyActivationTable+" WHERE credential_id = ? AND deactivated_at_ms = 0", "emergency-offline").Scan(&activated).Error; err != nil {
		t.Fatal(err)
	}
	if activated != 1 {
		t.Fatalf("activation rows=%d want1", activated)
	}
	var events int64
	if err := db.Raw("SELECT COUNT(*) FROM "+adminCredentialEventsTable+" WHERE credential_id = ? AND lifecycle = 'activate'", "emergency-offline").Scan(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("activate events=%d want1", events)
	}
	out.Reset()
	if err := run(context.Background(), []string{"deactivate", "--credential-id", "emergency-offline", "--reason", "dispatch complete"}, &out); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	var open int64
	if err := db.Raw("SELECT COUNT(*) FROM "+adminEmergencyActivationTable+" WHERE credential_id = ? AND deactivated_at_ms = 0", "emergency-offline").Scan(&open).Error; err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatal("deactivate did not close the activation")
	}
	// A remote/HTTP-shaped flag is refused: this is a local primitive.
	if err := run(context.Background(), []string{"activate", "--url", "https://evil.example"}, &out); err == nil {
		t.Fatal("remote activation flag accepted")
	}
}
