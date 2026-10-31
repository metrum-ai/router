// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResponseAffinityAsyncWriterCollisionAndFailure(t *testing.T) {
	store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	store.writer = newUsageWriter(store)
	defer store.Close()
	now := time.Now().UTC()
	row := responseAffinityRecord{ResponseID: "id", RequestID: "request", UpstreamResponseID: "upstream", TargetKey: "target", ModelGroup: "group", CallerID: "owner", Status: "completed", CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	store.recordResponseAffinity(row)
	if err := store.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := lookupOwnedResponseAffinity(store.db, row.ResponseID, "owner", now)
	if err != nil || got == nil {
		t.Fatalf("missing: %v %v", got, err)
	}
	row.CallerID = "other"
	store.recordResponseAffinity(row)
	if err := store.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = lookupOwnedResponseAffinity(store.db, row.ResponseID, "owner", now)
	if err != nil || got != nil {
		t.Fatalf("collision not revoked: %v %v", got, err)
	}
	if !strings.Contains(store.writer.prometheus(), "response_affinity_write_failures_total 1") {
		t.Fatalf("failure counter not updated")
	}
	row.ResponseID = "invalid"
	row.TargetKey = ""
	store.recordResponseAffinity(row)
	if err := store.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.writer.affinityFailures.Load() != 2 {
		t.Fatalf("failure count = %d", store.writer.affinityFailures.Load())
	}
}
