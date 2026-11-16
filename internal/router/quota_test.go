// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
)

// QD-1: save failure on RecordTokens keeps consumed usage as liability and latches admission.
func TestQuotaRecordTokensSaveFailureKeepsLiability(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	path := filepath.Join(dir, "state.json")
	qs, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	if _, _, err := qs.RecordTokens(caller, nil, Usage{TotalTokens: 3}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	before := qs.state.Callers["alice"].LifetimeTokens

	qs.saveFault = errors.New("injected save failure")
	quotaState, keyState, err := qs.RecordTokens(caller, nil, Usage{TotalTokens: 9})
	if !errors.Is(err, errQuotaStatePersist) {
		t.Fatalf("err=%v quota=%q key=%q, want errQuotaStatePersist", err, quotaState, keyState)
	}
	want := before + 9
	if qs.state.Callers["alice"].LifetimeTokens != want {
		t.Fatalf("lifetime_tokens=%d, want outstanding liability %d", qs.state.Callers["alice"].LifetimeTokens, want)
	}
	if !qs.PersistenceUnhealthy() {
		t.Fatal("expected persistence unhealthy latch after save failure")
	}
	blocked := qs.Admit(caller, 0)
	if blocked.OK || blocked.Status != http.StatusServiceUnavailable || blocked.Reason != "quota-state-error" {
		t.Fatalf("admit while unhealthy=%#v, want 503 quota-state-error", blocked)
	}

	qs.saveFault = nil
	recovered := qs.Admit(caller, 0)
	if !recovered.OK {
		t.Fatalf("admit after storage recovery=%#v", recovered)
	}
	qs.Release(caller)
	if qs.PersistenceUnhealthy() {
		t.Fatal("persistence latch should clear after successful recovery save")
	}
	qs2, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := qs2.state.Callers["alice"].LifetimeTokens; got != want {
		t.Fatalf("restart after recovery lifetime=%d, want durable liability %d", got, want)
	}
}

func TestMCPQuotaEnforcesCallerRateAndToolSpendAndPersists(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	path := filepath.Join(dir, "state.json")
	store, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := store.callers["alice"]
	first := store.AdmitMCP(caller, 1)
	if !first.OK {
		t.Fatalf("first MCP caller admission=%+v", first)
	}
	store.Release(caller)
	second := store.AdmitMCP(caller, 1)
	if second.OK || second.Status != http.StatusTooManyRequests || second.Reason != "rpm-exceeded" {
		t.Fatalf("second MCP caller admission=%+v", second)
	}
	limits := []MCPToolLimitConfig{
		{Caller: "alice", Tool: "", DailyUSD: .25, CostPerCallUSD: .15},
		{Caller: "alice", Tool: "docs/read", RPM: 10, HourlyUSD: .50, CostPerCallUSD: .15},
	}
	if admission := store.AdmitMCPTool("alice", "docs/read", limits); !admission.OK {
		t.Fatalf("first tool admission=%+v", admission)
	}
	blocked := store.AdmitMCPTool("alice", "docs/read", limits)
	if blocked.OK || blocked.Status != http.StatusTooManyRequests || blocked.Reason != "mcp-tool-spend-exceeded" {
		t.Fatalf("second tool admission=%+v, want spend limit", blocked)
	}
	reloaded, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if persisted := reloaded.AdmitMCPTool("alice", "docs/read", limits); persisted.OK || persisted.Reason != "mcp-tool-spend-exceeded" {
		t.Fatalf("persisted tool spend admission=%+v", persisted)
	}
}

// QD-2: exhaust persist path fails closed when save fails.
func TestQuotaExhaustSaveFailureFailsClosed(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].Key.LifetimeTokens = 10
	path := filepath.Join(dir, "state.json")
	qs, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	if _, _, err := qs.RecordTokens(caller, nil, Usage{TotalTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if !qs.state.Callers["alice"].Disabled {
		t.Fatal("expected caller disabled after lifetime exhaustion")
	}

	qs.state.Callers["alice"].Disabled = false
	qs.saveFault = errors.New("injected exhaust save failure")
	ad := qs.ReserveTokens(caller, 1)
	if ad.OK || ad.Status != http.StatusServiceUnavailable || ad.Reason != "quota-state-error" {
		t.Fatalf("reserve on exhaust save fault=%#v, want 503 quota-state-error", ad)
	}
	if qs.state.Callers["alice"].Disabled {
		t.Fatal("Disabled must roll back when exhaust save fails")
	}
	if !qs.PersistenceUnhealthy() {
		t.Fatal("expected persistence unhealthy after exhaust save failure")
	}

	admit := qs.Admit(caller, 0)
	if admit.OK || admit.Status != http.StatusServiceUnavailable || admit.Reason != "quota-state-error" {
		t.Fatalf("admit on exhaust save fault=%#v, want 503 quota-state-error", admit)
	}
}

// QD-3: ReleaseReservation restores reserved tokens.
func TestQuotaReleaseReservationRestoresReservedTokens(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].Quota.Day.Tokens = 1000
	qs, err := newQuotaStore(filepath.Join(dir, "state.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	res := qs.ReserveTokens(caller, 40)
	if !res.OK || res.Reservation == nil {
		t.Fatalf("reserve=%#v", res)
	}
	if caller.inFlightReservedTokens != 40 {
		t.Fatalf("reserved=%d, want 40", caller.inFlightReservedTokens)
	}
	qs.ReleaseReservation(caller, res.Reservation)
	if caller.inFlightReservedTokens != 0 {
		t.Fatalf("after release reserved=%d, want 0", caller.inFlightReservedTokens)
	}
	if res.Reservation.active {
		t.Fatal("reservation should be inactive after release")
	}
	again := qs.ReserveTokens(caller, 40)
	if !again.OK {
		t.Fatalf("re-reserve after cancel failed: %#v", again)
	}
	qs.ReleaseReservation(caller, again.Reservation)
}

// QD-4: failed upstream path still releases via ReleaseReservation (unit-level).
func TestQuotaReleaseReservationAfterFailedUpstreamPath(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].Quota.Day.Tokens = 50
	qs, err := newQuotaStore(filepath.Join(dir, "state.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]

	// Simulate handleLLM: reserve, upstream fails, defer ReleaseReservation.
	res := qs.ReserveTokens(caller, 40)
	if !res.OK {
		t.Fatalf("first reserve=%#v", res)
	}
	qs.ReleaseReservation(caller, res.Reservation)
	if caller.inFlightReservedTokens != 0 {
		t.Fatalf("leak after failed-upstream release: %d", caller.inFlightReservedTokens)
	}
	second := qs.ReserveTokens(caller, 40)
	if !second.OK {
		t.Fatalf("second reserve after failure release=%#v", second)
	}
	qs.ReleaseReservation(caller, second.Reservation)
}

// QD-5: restart restores successful saves; failed save keeps process-local liability latched.
func TestQuotaRestartSemanticsSuccessfulAndFailedSave(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	path := filepath.Join(dir, "state.json")
	qs, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	if _, _, err := qs.RecordTokens(caller, nil, Usage{TotalTokens: 5}); err != nil {
		t.Fatal(err)
	}
	if err := qs.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.state.Callers["alice"].LifetimeTokens; got != 5 {
		t.Fatalf("restart after success lifetime=%d, want 5", got)
	}

	reloaded.saveFault = errors.New("injected save failure")
	if _, _, err := reloaded.RecordTokens(reloaded.callers["alice"], nil, Usage{TotalTokens: 7}); !errors.Is(err, errQuotaStatePersist) {
		t.Fatalf("expected persist error, got %v", err)
	}
	if got := reloaded.state.Callers["alice"].LifetimeTokens; got != 12 {
		t.Fatalf("in-memory after failed save lifetime=%d, want outstanding 12", got)
	}
	if !reloaded.PersistenceUnhealthy() {
		t.Fatal("expected unhealthy latch")
	}

	again, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.state.Callers["alice"].LifetimeTokens; got != 5 {
		t.Fatalf("restart without recovery write lifetime=%d, want durable 5", got)
	}
}

// QD-6: concurrent Admit/Record on one caller does not double-count spend.
func TestQuotaConcurrentAdmitRecordNoDoubleCount(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].Rate.Concurrent = 32
	cfg.Callers[0].Quota.Day.Tokens = 1_000_000
	cfg.Callers[0].Quota.Month.Tokens = 1_000_000
	qs, err := newQuotaStore(filepath.Join(dir, "state.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]

	const workers = 20
	const tokensEach = 3
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ad := qs.Admit(caller, 0)
			if !ad.OK {
				errs <- errors.New("admit failed")
				return
			}
			defer qs.Release(caller)
			res := qs.ReserveTokens(caller, tokensEach)
			if !res.OK {
				errs <- errors.New("reserve failed")
				return
			}
			if _, _, err := qs.RecordTokens(caller, res.Reservation, Usage{TotalTokens: tokensEach}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	want := int64(workers * tokensEach)
	if got := qs.state.Callers["alice"].LifetimeTokens; got != want {
		t.Fatalf("lifetime_tokens=%d, want %d (no double-count)", got, want)
	}
	if caller.inFlightReservedTokens != 0 {
		t.Fatalf("reserved leak after concurrent records: %d", caller.inFlightReservedTokens)
	}
}

// QD-7: Admit persists request counts; save failure fails closed without leaking slots.
func TestQuotaAdmitPersistsRequestCounts(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].Quota.Day.Requests = 10
	path := filepath.Join(dir, "state.json")
	qs, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	ad := qs.Admit(caller, 0)
	if !ad.OK {
		t.Fatalf("admit=%#v", ad)
	}
	qs.Release(caller)
	reloaded, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.state.Callers["alice"].DayRequests; got != 1 {
		t.Fatalf("day_requests after restart=%d, want 1", got)
	}

	qs.saveFault = errors.New("admit save failure")
	before := qs.state.Callers["alice"].DayRequests
	fail := qs.Admit(caller, 0)
	if fail.OK || fail.Reason != "quota-state-error" {
		t.Fatalf("admit save fault=%#v", fail)
	}
	if qs.state.Callers["alice"].DayRequests != before {
		t.Fatalf("day_requests=%d, want rolled back to %d for pre-upstream failure", qs.state.Callers["alice"].DayRequests, before)
	}
	if caller.inFlight != 0 {
		t.Fatalf("inFlight leak=%d", caller.inFlight)
	}
}

// QD-8: AcquireConcurrency propagates exhausted-state save failures.
func TestQuotaAcquireConcurrencyExhaustSaveFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].Key.LifetimeTokens = 1
	qs, err := newQuotaStore(filepath.Join(dir, "state.json"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	if _, _, err := qs.RecordTokens(caller, nil, Usage{TotalTokens: 1}); err != nil {
		t.Fatal(err)
	}
	qs.state.Callers["alice"].Disabled = false
	qs.saveFault = errors.New("acquire save failure")
	ad := qs.AcquireConcurrency(caller)
	if ad.OK || ad.Status != http.StatusServiceUnavailable || ad.Reason != "quota-state-error" {
		t.Fatalf("acquire=%#v, want 503 quota-state-error", ad)
	}
}

func TestQuotaAcquireConcurrencyIgnoresInFlightTokenEstimates(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Callers[0].Key.LifetimeTokens = 100
	qs, err := newQuotaStore("", cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	reserved := qs.ReserveTokens(caller, 100)
	if !reserved.OK {
		t.Fatalf("reserve=%#v", reserved)
	}
	ad := qs.AcquireConcurrency(caller)
	if !ad.OK || ad.KeyState != "active" {
		t.Fatalf("acquire=%#v, want active despite temporary estimate", ad)
	}
	qs.ReleaseReservation(caller, reserved.Reservation)
	if qs.state.Callers["alice"].Disabled {
		t.Fatal("temporary estimate persisted the caller as exhausted")
	}
}
