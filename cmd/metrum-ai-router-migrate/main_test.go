// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/router"
	"gorm.io/gorm"
)

func TestRequireServingCompatible(t *testing.T) {
	tests := []struct {
		name   string
		status router.MigrationStatus
		want   bool
	}{
		{name: "pending ledger", status: router.MigrationStatus{Compatible: true, State: "pending"}},
		{name: "incompatible ledger", status: router.MigrationStatus{Compatible: false, State: "current"}},
		{name: "unvalidated job", status: router.MigrationStatus{Compatible: true, State: "current", Jobs: []router.MigrationDataJobStatus{{State: "pending"}}}},
		{name: "current validated ledger", status: router.MigrationStatus{Compatible: true, State: "current", Jobs: []router.MigrationDataJobStatus{{State: "validated"}}}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := requireServingCompatible(tc.status)
			if (err == nil) != tc.want {
				t.Fatalf("requireServingCompatible(%+v) error = %v, want success %t", tc.status, err, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), "migration verify-serving requires current compatible ledger and validated data jobs") {
				t.Fatalf("error = %q", err)
			}
		})
	}
}

type pendingDocument struct {
	Scope      string
	Compatible bool
	State      string
	Pending    []struct {
		ID             int
		Name           string
		Checksum       string
		ManifestDigest string
		ExecutionMode  string
		DataJobKey     string
		RollbackClass  string
	}
}

func decodeStatusDocument(t *testing.T, encoded []byte) pendingDocument {
	t.Helper()
	if !json.Valid(encoded) {
		t.Fatalf("status --json is not valid JSON: %q", string(encoded))
	}
	var document pendingDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode status document %q: %v", string(encoded), err)
	}
	return document
}

// A pending migration carries handler functions that encoding/json cannot
// marshal. The document must describe it with safe metadata instead of dropping
// the encoding error and printing an empty line.
func TestMigrationStatusJSONIncludesPendingMetadata(t *testing.T) {
	status := router.MigrationStatus{
		Scope:         "usage",
		SchemaVersion: 11,
		DataVersion:   1,
		Compatible:    true,
		State:         "pending",
		Pending: []router.MigrationDefinition{{
			ID:               2026110601,
			Scope:            "usage",
			Name:             "usage-ledger-schema-12",
			Release:          "v4.7.0",
			Checksum:         "6f1c0a4d0f6a2d3b1c9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b7c",
			ManifestDigest:   "sha256:0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e",
			SchemaVersion:    12,
			DataVersion:      1,
			Transactional:    true,
			ExecutionMode:    "transactional",
			MaintenanceMode:  "online",
			LockClass:        "online",
			TimeoutClass:     "bounded",
			RollbackClass:    "restore-required",
			DataJobKey:       "historical-usage-validation-v1",
			PostconditionKey: "usageLedgerSchema12@postcondition",
			Dependencies:     []int{2026110301},
			Apply:            func(*gorm.DB) error { return nil },
			Verify:           func(*gorm.DB) error { return nil },
		}},
	}
	encoded, err := migrationStatusJSON(status)
	if err != nil {
		t.Fatalf("migrationStatusJSON() error = %v", err)
	}
	document := decodeStatusDocument(t, encoded)
	if document.State != "pending" {
		t.Fatalf("state = %q, want pending", document.State)
	}
	if len(document.Pending) != 1 {
		t.Fatalf("pending entries = %d, want 1: %s", len(document.Pending), string(encoded))
	}
	pending := document.Pending[0]
	if pending.ID != 2026110601 {
		t.Fatalf("pending id = %d, want 2026110601", pending.ID)
	}
	if pending.Checksum != status.Pending[0].Checksum || pending.ManifestDigest != status.Pending[0].ManifestDigest {
		t.Fatalf("pending integrity metadata = %q/%q, want the declared checksum and manifest digest", pending.Checksum, pending.ManifestDigest)
	}
	if pending.ExecutionMode != "transactional" || pending.DataJobKey != "historical-usage-validation-v1" || pending.RollbackClass != "restore-required" {
		t.Fatalf("pending execution metadata = %+v, want execution mode, data-job key and rollback class", pending)
	}
	for _, forbidden := range []string{"func(", "gorm.DB"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("status document leaks handler detail %q: %s", forbidden, string(encoded))
		}
	}
}

// Regression test for the documented deployment-job gate: plan and status are
// read while a migration is pending, and both must print a readable document.
func TestStatusJSONWithPendingMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the migrate CLI")
	}
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	for _, action := range []string{"plan", "status"} {
		t.Run(action, func(t *testing.T) {
			command := exec.Command("go", "run", ".", "--driver=sqlite", "--db="+dbPath, "--action="+action, "--json")
			var stdout, stderr strings.Builder
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("migrate --action=%s --json failed: %v: %s", action, err, stderr.String())
			}
			output := strings.TrimSpace(stdout.String())
			if output == "" {
				t.Fatal("migrate --action=" + action + " --json printed nothing while a migration was pending")
			}
			document := decodeStatusDocument(t, []byte(output))
			if document.Scope != "usage" {
				t.Fatalf("scope = %q, want usage", document.Scope)
			}
			if document.State != "pending" {
				t.Fatalf("state = %q, want pending on a database with unapplied migrations", document.State)
			}
			if len(document.Pending) == 0 {
				t.Fatalf("pending entries = 0, want the unapplied migrations: %s", output)
			}
			for _, pending := range document.Pending {
				if pending.ID == 0 {
					t.Fatalf("pending migration without an id: %s", output)
				}
			}
		})
	}
}
