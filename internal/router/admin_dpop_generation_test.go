// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two validator configurations represent overlapping captured generations;
// the replay ledger belongs to the node, not either immutable configuration.
func TestRuntimeDPoPGenerationReplayAndCapacityShrink(t *testing.T) {
	state := newAdminDPoPReplayState()
	older, err := newAdminDPoPValidatorWithReplay("https://admin.example", 2*time.Minute, 2, state)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := newAdminDPoPValidatorWithReplay("https://admin.example", time.Minute, 1, state)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	older.now = func() time.Time { return now }
	newer.now = older.now
	key := dpopTestKey(t)
	thumb := dpopTestThumbprint(t, key)
	token := "access-token"
	request := func(id string) *http.Request {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "DPoP "+token)
		r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(token, id, now)))
		return r
	}
	first := request("generation-proof-first-000001")
	if err := older.Validate(context.Background(), first, token, thumb); err != nil {
		t.Fatal(err)
	}
	if err := newer.Validate(context.Background(), first, token, thumb); err == nil {
		t.Fatal("proof replayed after generation replacement")
	}
	second := request("generation-proof-second-00002")
	if err := newer.Validate(context.Background(), second, token, thumb); err == nil {
		t.Fatal("smaller capacity evicted live old-generation record")
	}
	if err := older.Validate(context.Background(), second, token, thumb); err != nil {
		t.Fatal("captured older generation could not use its remaining capacity", err)
	}
	now = now.Add(66 * time.Second)
	if err := newer.Validate(context.Background(), request("generation-proof-middle-00003"), token, thumb); err == nil {
		t.Fatal("shorter max age discarded longer old-generation retention")
	}
	if err := older.Validate(context.Background(), first, token, thumb); err == nil {
		t.Fatal("old accepted proof replayed inside old acceptance interval")
	}
	// Shared ledgers retain through the supported 5m acceptance ceiling so
	// a later max-age increase cannot revive already-claimed proofs.
	now = now.Add(240 * time.Second)
	if err := newer.Validate(context.Background(), request("generation-proof-after-000004"), token, thumb); err != nil {
		t.Fatal("expired records did not release smaller capacity", err)
	}
}

func TestRuntimeDPoPGenerationAgeIncreaseDoesNotReviveProof(t *testing.T) {
	state := newAdminDPoPReplayState()
	short, err := newAdminDPoPValidatorWithReplay("https://admin.example", time.Minute, 10, state)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	short.now = func() time.Time { return now }
	key := dpopTestKey(t)
	token := "access-token"
	thumb := dpopTestThumbprint(t, key)
	request := func(id string) *http.Request {
		r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
		r.Header.Set("Authorization", "DPoP "+token)
		r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(token, id, now)))
		return r
	}
	original := request("generation-age-growth-000001")
	if err := short.Validate(context.Background(), original, token, thumb); err != nil {
		t.Fatal(err)
	}
	now = now.Add(66 * time.Second)
	if err := short.Validate(context.Background(), request("generation-age-cleanup-00002"), token, thumb); err != nil {
		t.Fatal(err)
	}
	longer, err := newAdminDPoPValidatorWithReplay("https://admin.example", 2*time.Minute, 10, state)
	if err != nil {
		t.Fatal(err)
	}
	longer.now = short.now
	if err := longer.Validate(context.Background(), original, token, thumb); err == nil {
		t.Fatal("max-age increase revived previously accepted proof after shorter window expired")
	}
}

func TestRuntimeDPoPGenerationConcurrentClaim(t *testing.T) {
	state := newAdminDPoPReplayState()
	first, err := newAdminDPoPValidatorWithReplay("https://admin.example", time.Minute, 10, state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newAdminDPoPValidatorWithReplay("https://admin.example", 2*time.Minute, 10, state)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	first.now = func() time.Time { return now }
	second.now = first.now
	key := dpopTestKey(t)
	token := "access-token"
	thumb := dpopTestThumbprint(t, key)
	r := httptest.NewRequest("GET", "https://admin.example/admin/v1/config", nil)
	r.Header.Set("Authorization", "DPoP "+token)
	r.Header.Set("DPoP", dpopTestProof(t, key, "dpop+jwt", dpopTestClaims(token, "generation-concurrent-0000001", now)))
	var successes atomic.Int64
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := first
			if i%2 != 0 {
				v = second
			}
			if v.Validate(context.Background(), r, token, thumb) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("overlapping generations accepted same proof %d times", successes.Load())
	}
}
