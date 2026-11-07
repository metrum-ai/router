// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"
	"time"
)

func TestPolicyFreshnessUsesSuccessfulSyncAge(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	state := newPolicyFreshnessState(30 * time.Second)
	if !state.stale(now) {
		t.Fatal("unknown sync must fail closed")
	}
	state.observeSuccess(4, now)
	if state.stale(now.Add(30 * time.Second)) {
		t.Fatal("age equal to limit is allowed")
	}
	if !state.stale(now.Add(30*time.Second + time.Nanosecond)) {
		t.Fatal("age above limit must be stale even when revisions match")
	}
	state.observeDesired(5) // A read/build/publication failure cannot acknowledge sync.
	desired, applied, at, ok := state.PolicyFreshness()
	if desired != 5 || applied != 4 || !at.Equal(now) || !ok {
		t.Fatalf("failed apply changed observation: %d %d %v %v", desired, applied, at, ok)
	}
	if !state.stale(now.Add(31 * time.Second)) {
		t.Fatal("failed apply refreshed successful-sync clock")
	}
	state.observeSuccess(5, now.Add(32*time.Second))
	state.observeSuccess(5, now.Add(60*time.Second)) // Successful unchanged poll.
	if state.stale(now.Add(61 * time.Second)) {
		t.Fatal("unchanged successful sync did not recover freshness")
	}
	_, _, at, _ = state.PolicyFreshness()
	if !at.Equal(now.Add(60 * time.Second)) {
		t.Fatal("successful unchanged sync did not refresh observation")
	}
}

func TestPolicyFreshnessDefaultAndClockRollback(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	state := newPolicyFreshnessState(0)
	state.observeSuccess(1, now)
	if state.stale(now.Add(30 * time.Second)) {
		t.Fatal("default must be 30 seconds")
	}
	if !state.stale(now.Add(31 * time.Second)) {
		t.Fatal("default age limit not enforced")
	}
	if !state.stale(now.Add(-time.Second)) {
		t.Fatal("future observation cannot prove freshness")
	}
}

func TestPolicyFreshnessConfigurationDoesNotAcknowledgeSync(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	s := &Service{policyFreshness: newPolicyFreshnessState(0)}
	s.policyFreshness.observeSuccess(4, now)
	if err := s.ConfigureAdminPolicyFreshness(time.Second); err != nil {
		t.Fatal(err)
	}
	if !s.policyFreshness.stale(now.Add(2 * time.Second)) {
		t.Fatal("configured max age not applied")
	}
	_, _, observed, _ := s.PolicyFreshness()
	if !observed.Equal(now) {
		t.Fatal("threshold change acknowledged a sync")
	}
	for _, invalid := range []time.Duration{0, -time.Second} {
		if err := s.ConfigureAdminPolicyFreshness(invalid); err == nil {
			t.Fatal("nonpositive max age accepted")
		}
	}
}
