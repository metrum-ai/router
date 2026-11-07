// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"
	"time"
)

func TestQuotaCallerPreparationDetachedAndCommitKeepsLatestAccounting(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Callers[0].Rate.RPM = 120
	old := &callerRuntime{cfg: CallerConfig{ID: "alice"}, inFlight: 1}
	persistent := &callerState{DayTokens: 10, LifetimeTokens: 20}
	q := &quotaStore{callers: map[string]*callerRuntime{"alice": old, "retired": {cfg: CallerConfig{ID: "retired"}}}, state: persistentState{Callers: map[string]*callerState{"alice": persistent, "retired": {DayTokens: 3}}}}
	prepared, err := prepareQuotaCallers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if q.callers["alice"] != old || q.callers["retired"] == nil || q.state.Callers["alice"] != persistent || q.state.Callers["retired"] == nil {
		t.Fatal("detached preparation mutated active directory/accounting")
	}
	if _, err := prepareQuotaCallers(nil); err == nil {
		t.Fatal("nil caller candidate accepted")
	}
	// Model serving activity between off-path preparation and final installation.
	now := time.Now().UTC()
	q.mu.Lock()
	old.inFlight = 4
	old.inFlightReservedTokens = 55
	old.reqTimes = []time.Time{now}
	old.tokenTimes = []tokenEvent{{At: now, Tokens: 9}}
	persistent.DayTokens = 77
	persistent.LifetimeTokens = 88
	q.mu.Unlock()
	q.installPreparedCallers(prepared)
	actual := q.callers["alice"]
	if actual == old || actual.cfg.Rate.RPM != 120 || actual.inFlight != 4 || actual.inFlightReservedTokens != 55 || len(actual.reqTimes) != 1 || !actual.reqTimes[0].Equal(now) || len(actual.tokenTimes) != 1 || actual.tokenTimes[0].Tokens != 9 {
		t.Fatal("commit lost current accounting or failed to install candidate limits")
	}
	if q.state.Callers["alice"] != persistent || persistent.DayTokens != 77 || persistent.LifetimeTokens != 88 || q.callers["retired"] != nil || q.state.Callers["retired"] != nil {
		t.Fatal("persistent state preservation/removal differed from successful legacy rebuild")
	}
}
