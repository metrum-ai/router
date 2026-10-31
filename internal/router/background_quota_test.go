// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestBackgroundQuotaObservedAcrossReplicasAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenUsageStore(UsageDBConfig{Driver: "sqlite", Path: filepath.Join(dir, "usage.sqlite"), MigrationPolicy: usageDBMigrationPolicyAutoSafe})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].Key.LifetimeTokens = 20
	cfg.Callers[0].Rate.TPM = 0
	q1, err := newQuotaStore(filepath.Join(dir, "replica1.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer q1.Close()
	q2, err := newQuotaStore(filepath.Join(dir, "replica2.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	q1.backgroundDB = store.db
	q2.backgroundDB = store.db
	now := time.Now().UTC()
	snapshot, _ := json.Marshal(backgroundBillingSnapshot{Provider: "mock", Model: "model", InputPrice: 1, OutputPrice: 2})
	row := responseAffinityRecord{ResponseID: "bg", UpstreamResponseID: "provider", TargetKey: "key", ModelGroup: "default", CallerID: "alice", Status: "in_progress", Background: true, CreatedAt: now.Add(-time.Second).UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(), BillingSnapshot: string(snapshot)}
	if err := insertResponseAffinity(store.db, row); err != nil {
		t.Fatal(err)
	}
	if changed, err := settleBackgroundResponse(context.Background(), store.db, "bg", "alice", "completed", Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, now); err != nil || !changed {
		t.Fatalf("settle: %v %v", changed, err)
	}
	for _, q := range []*quotaStore{q1, q2} {
		caller := q.callers["alice"]
		usage, err := q.UsageWithError(caller)
		if err != nil {
			t.Fatal(err)
		}
		if got := usage["key"].(map[string]any)["lifetime_tokens"]; got != int64(15) {
			t.Fatalf("replica lifetime: %v", got)
		}
		ad := q.ReserveTokens(caller, 6)
		if ad.OK || ad.Reason != "key-exhausted" {
			t.Fatalf("lifetime bypass: %+v", ad)
		}
		caller.cfg.Key.LifetimeTokens = 0
		caller.cfg.Rate.TPM = 20
		ad = q.ReserveTokens(caller, 6)
		if ad.OK || ad.Reason != "tpm-exceeded" {
			t.Fatalf("TPM bypass: %+v", ad)
		}
		caller.cfg.Rate.TPM = 0
		caller.cfg.Quota.Day.Tokens = 20
		ad = q.ReserveTokens(caller, 6)
		if ad.OK || ad.Reason != "quota-exhausted" {
			t.Fatalf("day quota bypass: %+v", ad)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if ad := q1.Admit(q1.callers["alice"], 1); ad.OK || ad.Status != 503 {
		t.Fatalf("DB outage admitted: %+v", ad)
	}
	if _, err := q1.UsageWithError(q1.callers["alice"]); err == nil {
		t.Fatal("DB outage returned incomplete quota")
	}
}
