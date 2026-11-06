// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSolverPresentsAndCleansUpExactRecord(t *testing.T) {
	provider := &memProvider{name: "fake", zones: []string{"example.com"}}
	var mu sync.Mutex
	calls := 0
	opts := Options{
		TTL:                60,
		PropagationTimeout: time.Second,
		LookupTXT: func(_ context.Context, name string) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if name != "_acme-challenge.router.example.com" {
				t.Errorf("lookup name = %q", name)
			}
			if calls < 2 {
				return nil, nil // not visible yet
			}
			return []string{"challenge-value"}, nil
		},
	}
	solver := NewSolver(provider, "example.com", opts)
	if err := solver.Present(context.Background(), "router.example.com", "challenge-value"); err != nil {
		t.Fatalf("present: %v", err)
	}
	if len(provider.records) != 1 {
		t.Fatalf("records = %+v", provider.records)
	}
	record := provider.records[0]
	if record.Type != "TXT" || record.Name != "_acme-challenge.router.example.com" || record.Value != "challenge-value" || record.TTL != 60 {
		t.Fatalf("presented record = %+v", record)
	}
	if err := solver.Cleanup(context.Background(), "router.example.com", "challenge-value"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(provider.records) != 0 {
		t.Fatalf("record was not removed: %+v", provider.records)
	}
}

func TestSolverWildcardChallengeName(t *testing.T) {
	provider := &memProvider{name: "fake", zones: []string{"example.com"}}
	solver := NewSolver(provider, "example.com", Options{
		TTL:                60,
		PropagationTimeout: time.Second,
		LookupTXT:          func(context.Context, string) ([]string, error) { return []string{"v"}, nil },
	})
	if err := solver.Present(context.Background(), "*.example.com", "v"); err != nil {
		t.Fatalf("present: %v", err)
	}
	if got := provider.records[0].Name; got != "_acme-challenge.example.com" {
		t.Fatalf("wildcard challenge name = %q", got)
	}
}

func TestSolverCleanupOnlyDeletesMatchingValue(t *testing.T) {
	provider := &memProvider{
		name:  "fake",
		zones: []string{"example.com"},
		records: []Record{
			{ID: "other", Type: "TXT", Name: "_acme-challenge.example.com", Value: "someone-else", TTL: 60},
		},
	}
	solver := NewSolver(provider, "example.com", Options{TTL: 60, PropagationTimeout: time.Second})
	if err := solver.Cleanup(context.Background(), "example.com", "our-value"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if provider.deletes != 0 {
		t.Fatalf("cleanup deleted %d records, want 0", provider.deletes)
	}
	if len(provider.records) != 1 {
		t.Fatalf("foreign record removed: %+v", provider.records)
	}
}

func TestSolverPropagationTimeoutRemovesItsRecord(t *testing.T) {
	provider := &memProvider{name: "fake", zones: []string{"example.com"}}
	instruments := &countingInstruments{}
	solver := NewSolver(provider, "example.com", Options{
		TTL:                60,
		PropagationTimeout: time.Nanosecond,
		Instruments:        instruments,
		LookupTXT:          func(context.Context, string) ([]string, error) { return nil, nil },
	})
	if err := solver.Present(context.Background(), "example.com", "v"); err == nil {
		t.Fatal("expected a propagation error")
	}
	if len(provider.records) != 0 {
		t.Fatalf("the challenge record leaked after a propagation failure: %+v", provider.records)
	}
	if provider.deletes != 1 {
		t.Fatalf("delete calls = %d, want 1", provider.deletes)
	}
}

func TestSolverCleanupLeavesIdenticalForeignRecordAlone(t *testing.T) {
	// A record with the same name and value that this solver did not create is
	// never removed: only the ids the solver recorded are deleted.
	provider := &memProvider{
		name:  "fake",
		zones: []string{"example.com"},
		records: []Record{
			{ID: "foreign", Type: "TXT", Name: "_acme-challenge.example.com", Value: "v", TTL: 60},
		},
	}
	solver := NewSolver(provider, "example.com", Options{TTL: 60, PropagationTimeout: time.Second})
	if err := solver.Cleanup(context.Background(), "example.com", "v"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if provider.deletes != 0 || len(provider.records) != 1 {
		t.Fatalf("an unowned record was removed: deletes=%d records=%+v", provider.deletes, provider.records)
	}
}

func TestSolverPropagationTimeoutRaisesMetric(t *testing.T) {
	provider := &memProvider{name: "fake", zones: []string{"example.com"}}
	instruments := &countingInstruments{}
	solver := NewSolver(provider, "example.com", Options{
		TTL:                60,
		PropagationTimeout: time.Nanosecond,
		Instruments:        instruments,
		LookupTXT:          func(context.Context, string) ([]string, error) { return nil, nil },
	})
	err := solver.Present(context.Background(), "example.com", "v")
	if err == nil || !strings.Contains(err.Error(), "did not become visible") {
		t.Fatalf("error = %v", err)
	}
	if instruments.presentFailures != 1 {
		t.Fatalf("present failures = %d, want 1", instruments.presentFailures)
	}
}

func TestMatchZone(t *testing.T) {
	zones := []string{"example.com", "sub.example.com", "other.net"}
	cases := []struct {
		name       string
		domains    []string
		configured string
		want       string
		wantErr    bool
	}{
		{name: "longest suffix wins", domains: []string{"router.sub.example.com"}, want: "sub.example.com"},
		{name: "apex domain", domains: []string{"example.com"}, want: "example.com"},
		{name: "configured zone", domains: []string{"router.example.com"}, configured: "example.com", want: "example.com"},
		{name: "wildcard", domains: []string{"*.example.com"}, want: "example.com"},
		{name: "no match", domains: []string{"router.unknown.org"}, wantErr: true},
		{name: "configured zone is not a suffix", domains: []string{"router.example.com"}, configured: "other.net", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchZone(tc.domains, tc.configured, zones)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("zone = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRelativeAndAbsoluteNames(t *testing.T) {
	if got := relativeName("_acme-challenge.example.com", "example.com"); got != "_acme-challenge" {
		t.Fatalf("relative = %q", got)
	}
	if got := relativeName("example.com", "example.com"); got != "@" {
		t.Fatalf("apex relative = %q", got)
	}
	if got := absoluteName("_acme-challenge", "example.com"); got != "_acme-challenge.example.com" {
		t.Fatalf("absolute = %q", got)
	}
	if got := absoluteName("@", "example.com"); got != "example.com" {
		t.Fatalf("apex absolute = %q", got)
	}
	if got := absoluteName("_acme-challenge.sub.example.com", "example.com"); got != "_acme-challenge.sub.example.com" {
		t.Fatalf("already absolute = %q", got)
	}
}
