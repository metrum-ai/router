// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUsageMissionAttributionMigrationAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	old, closeDB, err := UsageMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	priorDefinitions := make([]MigrationDefinition, 0, len(usageMigrationDefinitions)-2)
	for _, definition := range usageMigrationDefinitions {
		if definition.ID != usageMissionAttributionMigrationID && definition.ID != toolRoutingUsageMigrationID {
			priorDefinitions = append(priorDefinitions, definition)
		}
	}
	priorSchema := UsageMissionAttributionMigrationDefinition().SchemaVersion - 1
	oldRunner, err := NewMigrationRunnerWithDataJobs(old.db, usageMigrationScope, MigrationCompatibility{MinSchema: 0, MaxSchema: priorSchema, MinData: 0, MaxData: 1}, priorDefinitions, usageDataJobDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldRunner.ApplyPending("migration-test-v15"); err != nil {
		t.Fatal(err)
	}
	status, err := oldRunner.Status()
	if err != nil || status.SchemaVersion != priorSchema {
		t.Fatalf("prior migration set did not reach schema %d: %+v err=%v", priorSchema, status, err)
	}
	store := &usageStore{db: old.db}
	// The baseline migration creates tables from current Go models on a fresh
	// store, so remove the new columns to reproduce the deployed pre-migration shape.
	for _, column := range []string{"TaskID", "HumanOwner"} {
		if old.db.Migrator().HasColumn(&usageRecord{}, column) {
			if err := old.db.Migrator().DropColumn(&usageRecord{}, column); err != nil {
				t.Fatal(err)
			}
		}
	}
	legacy := logRecord{RequestID: "before-mission-attribution", TS: time.Now().UTC().Format(time.RFC3339Nano), CallerID: "worker"}
	if err := store.db.Omit("TaskID", "HumanOwner", "ToolCallCount", "ToolFirstFailureClass", "ToolRetries", "ToolEscalations", "ToolReliabilityBucket", "ToolReliabilitySource").Create(recordFromRow(rowFromRecord(legacy))).Error; err != nil {
		t.Fatalf("write row before migration: %v", err)
	}
	currentRunner, err := newUsageMigrationRunner(store.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := currentRunner.ApplyPending("mission-attribution-upgrade"); err != nil {
		t.Fatalf("apply mission attribution migration: %v", err)
	}
	if err := verifyUsageMissionAttributionMigration(store.db); err != nil {
		t.Fatal(err)
	}
	rec := logRecord{RequestID: "mission-attribution", TS: time.Now().UTC().Format(time.RFC3339Nano), CallerID: "worker", TaskID: "task-7", HumanOwner: "team-ml"}
	if err := store.db.Create(recordFromRow(rowFromRecord(rec))).Error; err != nil {
		t.Fatal(err)
	}
	var stored usageRecord
	if err := store.db.First(&stored, "request_id = ?", rec.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	row, err := rowFromUsageRecord(stored)
	if err != nil {
		t.Fatal(err)
	}
	if row.TaskID != rec.TaskID || row.HumanOwner != rec.HumanOwner {
		t.Fatalf("mission attribution lost: %+v", row)
	}
	var preserved usageRecord
	if err := store.db.First(&preserved, "request_id = ?", legacy.RequestID).Error; err != nil {
		t.Fatalf("pre-migration usage row was not preserved: %v", err)
	}
}
