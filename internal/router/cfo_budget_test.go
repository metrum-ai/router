// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCFOBudgetSnapshotScopeAndUnlimited(t *testing.T) {
	now := time.Now().UTC()
	makeCaller := func(id, project, token string) *callerRuntime {
		return &callerRuntime{cfg: CallerConfig{ID: id, Project: project, Environment: "test", TokenID: token, Quota: QuotaConfig{Day: BudgetConfig{Tokens: 100}}}}
	}
	store := &quotaStore{
		callers: map[string]*callerRuntime{"a": makeCaller("a", "finance", "key-a"), "b": makeCaller("b", "engineering", "key-b")},
		state:   persistentState{Callers: map[string]*callerState{"a": {DayStart: dayStart(now), MonthStart: monthStart(now), DayTokens: 40}}},
	}
	s := &Service{quota: store}
	result := s.cfoBudgets(adminAuthSubject{domain: "finance/test"}, false, UsageReportOptions{})
	if !result.Available || len(result.Rows) != 3 {
		t.Fatalf("bad scope: %+v", result)
	}
	for _, row := range result.Rows {
		if row.CallerID != "a" {
			t.Fatalf("leaked caller: %+v", row)
		}
		if row.Window == "day" && (row.UtilPct == nil || *row.UtilPct != 40) {
			t.Fatalf("bad utilization: %+v", row)
		}
		if row.Window != "day" && row.UtilPct != nil {
			t.Fatalf("unlimited budget reported percentage: %+v", row)
		}
	}
	filtered := s.cfoBudgets(adminAuthSubject{domain: "finance/test"}, false, UsageReportOptions{TokenIDPrefix: "missing"})
	if len(filtered.Rows) != 0 {
		t.Fatalf("bad token filter: %+v", filtered.Rows)
	}
}

func TestCFOBudgetTokenFilteringWithoutDisclosure(t *testing.T) {
	secret := "rtr_metrum_alice_finance_test_public_fake-secret"
	store := &quotaStore{callers: map[string]*callerRuntime{
		"structured": {cfg: CallerConfig{ID: "structured", TokenID: secret}},
		"legacy":     {cfg: CallerConfig{ID: "legacy", TokenSHA256: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}},
	}, state: persistentState{Callers: map[string]*callerState{}}}
	svc := &Service{quota: store}
	got := svc.cfoBudgets(adminAuthSubject{}, true, UsageReportOptions{TokenID: publicTokenID(secret)})
	if len(got.Rows) != 3 || got.Rows[0].CallerID != "structured" {
		t.Fatalf("wrong structured key selection: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), publicTokenID(secret)) || strings.Contains(string(encoded), "tokenId") {
		t.Fatal("quota snapshot disclosed a token ID")
	}
	legacy := svc.cfoBudgets(adminAuthSubject{}, true, UsageReportOptions{TokenID: "sha256:aaaaaaaaaaaa"})
	if len(legacy.Rows) != 3 || legacy.Rows[0].CallerID != "legacy" {
		t.Fatalf("wrong legacy fallback selection: %+v", legacy)
	}
	legacySecret := "plain-legacy-bearer-for-test"
	hash := legacyCallerTokenSHA256Hex(legacySecret)
	if cfoUsageTokenID(CallerConfig{TokenID: legacySecret, TokenSHA256: hash}) != legacySecret {
		t.Fatal("legacy matching must mirror the stored usage label")
	}
	if prefix := svc.cfoBudgets(adminAuthSubject{}, true, UsageReportOptions{TokenIDPrefix: "%"}); prefix.Available || len(prefix.Rows) != 0 {
		t.Fatalf("unsafe prefix filter: %+v", prefix)
	}
}

func TestCFOBudgetCanonicalOwnerAndDomain(t *testing.T) {
	store := &quotaStore{callers: map[string]*callerRuntime{"one": {cfg: CallerConfig{ID: "one", OwnerUser: "Alice Smith", Project: "Finance Team", Environment: "Production"}}}, state: persistentState{Callers: map[string]*callerState{}}}
	opts := UsageReportOptions{CallerUser: "alice-smith", CallerProject: "finance-team", CallerEnvironment: "production"}
	svc := &Service{quota: store}
	got := svc.cfoBudgets(adminAuthSubject{domain: "finance-team/production"}, false, opts)
	if len(got.Rows) != 3 || got.Rows[0].Project != "finance-team" {
		t.Fatalf("canonical caller mismatch: %+v", got)
	}
	denied := svc.cfoBudgets(adminAuthSubject{domain: "other/production"}, false, opts)
	if len(denied.Rows) != 0 {
		t.Fatalf("domain leaked quota: %+v", denied)
	}
}

func TestCFOBudgetSnapshotIsBounded(t *testing.T) {
	store := &quotaStore{callers: map[string]*callerRuntime{}, state: persistentState{Callers: map[string]*callerState{}}}
	for i := 0; i < 501; i++ {
		id := fmt.Sprintf("caller-%03d", i)
		store.callers[id] = &callerRuntime{cfg: CallerConfig{ID: id, Project: "finance", Environment: "test"}}
	}
	result := (&Service{quota: store}).cfoBudgets(adminAuthSubject{domain: "finance/test"}, false, UsageReportOptions{})
	if !result.Truncated || len(result.Rows) != 1500 || result.Rows[0].CallerID != "caller-000" || result.Rows[len(result.Rows)-1].CallerID != "caller-499" {
		t.Fatalf("unbounded or nondeterministic budget snapshot: truncated=%t rows=%d", result.Truncated, len(result.Rows))
	}
}
