// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Multiple callers race independently; all successful reservations stay held
// until every admission has completed, avoiding scheduler-dependent results.
func TestGovernanceLoadCallerIsolation(t *testing.T) {
	const callers, workers, estimate, actual, perCallerLimit = 4, 128, 10, 15, 8
	cfg := testConfig(t, "http://127.0.0.1:1", "synthetic", t.TempDir())
	base := cfg.Callers[0]
	cfg.Callers = nil
	for i := 0; i < callers; i++ {
		c := base
		c.ID = fmt.Sprintf("caller-%d", i)
		c.TokenID = fmt.Sprintf("synthetic-token-%d", i)
		c.Rate = RateConfig{RPM: workers, Concurrent: workers}
		c.Quota = QuotaConfig{}
		c.Quota.Day.Tokens = perCallerLimit * estimate
		c.Key = KeyConfig{}
		cfg.Callers = append(cfg.Callers, c)
	}
	store, err := newQuotaStore("", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	type result struct {
		index     int
		admission admission
	}
	results := make(chan result, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		index := i % callers
		go func() {
			<-start
			caller := store.callers[fmt.Sprintf("caller-%d", index)]
			results <- result{index, store.Admit(caller, estimate)}
		}()
	}
	close(start)
	accepted := [callers][]*quotaReservation{}
	rejected := [callers]int{}
	for i := 0; i < workers; i++ {
		select {
		case result := <-results:
			if result.admission.OK {
				accepted[result.index] = append(accepted[result.index], result.admission.Reservation)
			} else {
				rejected[result.index]++
				if result.admission.Reason != "quota-exhausted" {
					t.Errorf("unexpected reason=%s", result.admission.Reason)
				}
			}
		case <-time.After(10 * time.Second):
			t.Fatal("caller admissions timed out")
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < callers; i++ {
		if len(accepted[i]) != perCallerLimit || rejected[i] != workers/callers-perCallerLimit {
			t.Fatalf("caller=%d admitted=%d rejected=%d", i, len(accepted[i]), rejected[i])
		}
		caller := store.callers[fmt.Sprintf("caller-%d", i)]
		for _, reservation := range accepted[i] {
			wg.Add(1)
			go func(r *quotaReservation) {
				defer wg.Done()
				_, _, err := store.RecordTokens(caller, r, Usage{TotalTokens: actual})
				if err != nil {
					errs <- err
				}
				store.Release(caller)
			}(reservation)
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("caller settlement timed out")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for i := 0; i < callers; i++ {
		id := fmt.Sprintf("caller-%d", i)
		state, caller := store.state.Callers[id], store.callers[id]
		if state.DayTokens != perCallerLimit*actual || state.LifetimeTokens != perCallerLimit*actual || caller.inFlight != 0 || caller.inFlightReservedTokens != 0 {
			t.Fatalf("caller=%s day=%d lifetime=%d inflight=%d reserved=%d", id, state.DayTokens, state.LifetimeTokens, caller.inFlight, caller.inFlightReservedTokens)
		}
		t.Logf("caller=%s admitted=%d rejected=%d day_tokens=%d budget=%d over_budget=%d", id, len(accepted[i]), rejected[i], state.DayTokens, perCallerLimit*estimate, perCallerLimit*(actual-estimate))
	}
}
