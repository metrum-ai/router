// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestGovernanceLoadReservations is a deterministic in-process reservation
// stress test, not a substitute for HTTP, persistence, or distributed tests.
// Accepted reservations are held until every admission attempt completes.
func TestGovernanceLoadReservations(t *testing.T) {
	for _, workers := range []int{1, 32, 128} {
		for _, dimension := range []string{"day", "month", "lifetime", "tpm", "rpm"} {
			t.Run(fmt.Sprintf("%s/concurrency-%d", dimension, workers), func(t *testing.T) {
				cfg := testConfig(t, "http://127.0.0.1:1", "synthetic", t.TempDir())
				callerCfg := &cfg.Callers[0]
				callerCfg.Rate = RateConfig{RPM: workers + 10, Concurrent: workers + 10}
				callerCfg.Quota = QuotaConfig{}
				callerCfg.Key = KeyConfig{}
				acceptedLimit := workers / 4
				if acceptedLimit < 1 {
					acceptedLimit = 1
				}
				const estimate = 10
				const actual = 15
				budget := acceptedLimit * estimate
				var rejectReason string
				switch dimension {
				case "day":
					callerCfg.Quota.Day.Tokens = int64(budget)
					rejectReason = "quota-exhausted"
				case "month":
					callerCfg.Quota.Month.Tokens = int64(budget)
					rejectReason = "quota-exhausted"
				case "lifetime":
					callerCfg.Key.LifetimeTokens = int64(budget)
					rejectReason = "key-exhausted"
				case "tpm":
					callerCfg.Rate.TPM = budget
					rejectReason = "tpm-exceeded"
				case "rpm":
					callerCfg.Rate.RPM = acceptedLimit
					rejectReason = "rpm-exceeded"
				}
				store, err := newQuotaStore("", cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				caller := store.callers[callerCfg.ID]
				start := make(chan struct{})
				results := make(chan admission, workers)
				for i := 0; i < workers; i++ {
					go func() { <-start; results <- store.Admit(caller, estimate) }()
				}
				close(start)
				accepted := make([]*quotaReservation, 0, acceptedLimit)
				rejected := 0
				for i := 0; i < workers; i++ {
					select {
					case result := <-results:
						if result.OK {
							accepted = append(accepted, result.Reservation)
						} else {
							rejected++
							if result.Reason != rejectReason {
								t.Errorf("unexpected rejection: %s", result.Reason)
							}
						}
					case <-time.After(10 * time.Second):
						t.Fatal("admission deadlocked or exceeded 10 seconds")
					}
				}
				if len(accepted) != acceptedLimit || rejected != workers-acceptedLimit {
					t.Fatalf("accepted=%d rejected=%d, want %d/%d", len(accepted), rejected, acceptedLimit, workers-acceptedLimit)
				}
				var wg sync.WaitGroup
				errs := make(chan error, len(accepted))
				for _, reservation := range accepted {
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
				done := make(chan struct{})
				go func() { wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("settlement deadlocked or exceeded 10 seconds")
				}
				close(errs)
				for err := range errs {
					t.Error(err)
				}
				state := store.state.Callers[callerCfg.ID]
				want := int64(acceptedLimit * actual)
				if state.DayTokens != want || state.MonthTokens != want || state.LifetimeTokens != want || caller.inFlight != 0 || caller.inFlightReservedTokens != 0 {
					t.Fatalf("settlement mismatch day=%d month=%d lifetime=%d inflight=%d reserved=%d", state.DayTokens, state.MonthTokens, state.LifetimeTokens, caller.inFlight, caller.inFlightReservedTokens)
				}
				t.Logf("dimension=%s workers=%d admitted=%d rejected=%d estimate=%d actual=%d settled=%d over_estimate=%d", dimension, workers, len(accepted), rejected, estimate, actual, want, acceptedLimit*(actual-estimate))
			})
		}
	}
}
