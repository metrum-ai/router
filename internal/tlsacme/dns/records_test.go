// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// memProvider is an in-memory Provider used by the reconciliation and solver
// tests. Provider-specific tests assert the exact API payloads.
type memProvider struct {
	name    string
	zones   []string
	records []Record
	writes  int
	creates int
	updates int
	deletes int
	nextID  int
}

func (p *memProvider) Name() string { return p.name }

func (p *memProvider) Zones(context.Context) ([]string, error) { return p.zones, nil }

func (p *memProvider) Records(context.Context, string) ([]Record, error) {
	out := make([]Record, len(p.records))
	copy(out, p.records)
	return out, nil
}

func (p *memProvider) Create(_ context.Context, _ string, record Record) (string, error) {
	p.writes++
	p.creates++
	p.nextID++
	id := fmt.Sprintf("id-%d", p.nextID)
	record.ID = id
	p.records = append(p.records, record)
	return id, nil
}

func (p *memProvider) Update(_ context.Context, _ string, id string, record Record) error {
	p.writes++
	p.updates++
	for i := range p.records {
		if p.records[i].ID == id {
			record.ID = id
			p.records[i] = record
			return nil
		}
	}
	return fmt.Errorf("record %s not found", id)
}

func (p *memProvider) Delete(_ context.Context, _ string, id string) error {
	p.writes++
	p.deletes++
	for i := range p.records {
		if p.records[i].ID == id {
			p.records = append(p.records[:i], p.records[i+1:]...)
			return nil
		}
	}
	return nil
}

type countingInstruments struct {
	presentFailures int
	conflicts       map[string]int
	managed         float64
}

func (c *countingInstruments) DNSPresentFailure(string) { c.presentFailures++ }
func (c *countingInstruments) DNSConflict(_, reason string) {
	if c.conflicts == nil {
		c.conflicts = map[string]int{}
	}
	c.conflicts[reason]++
}
func (c *countingInstruments) DNSRecordsManaged(_, _ string, delta float64) { c.managed += delta }

func TestReconcileCreatesRecordAndMarkerOnce(t *testing.T) {
	provider := &memProvider{name: "fake", zones: []string{"example.com"}}
	instruments := &countingInstruments{}
	opts := Options{Instruments: instruments}
	want := Record{Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300}

	result, err := Reconcile(context.Background(), provider, "example.com", want, "host-1", opts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Created != 1 || result.Conflicts != 0 {
		t.Fatalf("result = %+v, want one create", result)
	}
	if provider.creates != 2 {
		t.Fatalf("creates = %d, want 2 (record + ownership marker)", provider.creates)
	}
	var marker *Record
	for i := range provider.records {
		if provider.records[i].Type == "TXT" {
			marker = &provider.records[i]
		}
	}
	if marker == nil {
		t.Fatal("no ownership marker was created")
	}
	if marker.Name != "_metrum-router.router.example.com" {
		t.Fatalf("marker name = %q", marker.Name)
	}
	if want := "metrum-ai-router-managed=1;owner=host-1;name=router.example.com;type=A"; marker.Value != want {
		t.Fatalf("marker value = %q, want %q", marker.Value, want)
	}
	if instruments.managed != 1 {
		t.Fatalf("managed gauge = %v, want 1", instruments.managed)
	}

	// A second run changes nothing and makes no write call (ACP-N08c).
	provider.writes = 0
	provider.creates = 0
	result, err = Reconcile(context.Background(), provider, "example.com", want, "host-1", opts)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if result.Unchanged != 1 || result.Managed() != 0 {
		t.Fatalf("second result = %+v, want unchanged", result)
	}
	if provider.writes != 0 {
		t.Fatalf("second run made %d write calls, want 0", provider.writes)
	}
}

func TestReconcileRefusesForeignRecord(t *testing.T) {
	provider := &memProvider{
		name:  "fake",
		zones: []string{"example.com"},
		records: []Record{
			{ID: "foreign", Type: "A", Name: "router.example.com", Value: "198.51.100.7", TTL: 300},
		},
	}
	instruments := &countingInstruments{}
	result, err := Reconcile(context.Background(), provider, "example.com",
		Record{Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300}, "host-1",
		Options{Instruments: instruments})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Conflicts != 1 || result.Managed() != 0 {
		t.Fatalf("result = %+v, want a conflict and no change", result)
	}
	if provider.writes != 0 {
		t.Fatalf("writes = %d, want 0 (a foreign record is never touched)", provider.writes)
	}
	if instruments.conflicts["unowned_record"] != 1 {
		t.Fatalf("conflict counter = %v", instruments.conflicts)
	}
	if provider.records[0].Value != "198.51.100.7" {
		t.Fatalf("foreign record was modified: %+v", provider.records[0])
	}
}

func TestReconcileRefusesForeignOwnerMarker(t *testing.T) {
	provider := &memProvider{
		name:  "fake",
		zones: []string{"example.com"},
		records: []Record{
			{ID: "rec", Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300},
			{ID: "marker", Type: "TXT", Name: "_metrum-router.router.example.com",
				Value: "metrum-ai-router-managed=1;owner=other-host;name=router.example.com;type=A", TTL: 300},
		},
	}
	instruments := &countingInstruments{}
	result, err := Reconcile(context.Background(), provider, "example.com",
		Record{Type: "A", Name: "router.example.com", Value: "203.0.113.99", TTL: 300}, "host-1",
		Options{Instruments: instruments})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Conflicts != 1 || provider.writes != 0 {
		t.Fatalf("result = %+v writes = %d, want a conflict and no writes", result, provider.writes)
	}
	if instruments.conflicts["foreign_marker"] != 1 {
		t.Fatalf("conflict counter = %v", instruments.conflicts)
	}
}

func TestReconcileUpdatesOwnedRecord(t *testing.T) {
	provider := &memProvider{
		name:  "fake",
		zones: []string{"example.com"},
		records: []Record{
			{ID: "rec", Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300},
			{ID: "marker", Type: "TXT", Name: "_metrum-router.router.example.com",
				Value: "metrum-ai-router-managed=1;owner=host-1;name=router.example.com;type=A", TTL: 300},
		},
	}
	result, err := Reconcile(context.Background(), provider, "example.com",
		Record{Type: "A", Name: "router.example.com", Value: "203.0.113.11", TTL: 300}, "host-1", Options{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Updated != 1 {
		t.Fatalf("result = %+v, want one update", result)
	}
	if provider.updates != 1 || provider.creates != 0 {
		t.Fatalf("updates = %d creates = %d, want 1 and 0", provider.updates, provider.creates)
	}
	if provider.records[0].Value != "203.0.113.11" {
		t.Fatalf("record value = %q", provider.records[0].Value)
	}
}

func TestDeleteManagedRequiresOwnership(t *testing.T) {
	provider := &memProvider{
		name:  "fake",
		zones: []string{"example.com"},
		records: []Record{
			{ID: "rec", Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300},
		},
	}
	want := Record{Type: "A", Name: "router.example.com"}
	removed, err := DeleteManaged(context.Background(), provider, "example.com", want, "host-1", Options{})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if removed || provider.deletes != 0 {
		t.Fatalf("removed = %v deletes = %d, want false and 0", removed, provider.deletes)
	}

	provider.records = append(provider.records, Record{
		ID: "marker", Type: "TXT", Name: "_metrum-router.router.example.com",
		Value: "metrum-ai-router-managed=1;owner=host-1;name=router.example.com;type=A", TTL: 300,
	})
	removed, err = DeleteManaged(context.Background(), provider, "example.com", want, "host-1", Options{})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !removed {
		t.Fatal("owned record was not removed")
	}
	if len(provider.records) != 0 {
		t.Fatalf("records left = %+v", provider.records)
	}
}

func TestReconcileRejectsOutOfZoneRecord(t *testing.T) {
	provider := &memProvider{name: "fake", zones: []string{"example.com"}}
	_, err := Reconcile(context.Background(), provider, "example.com",
		Record{Type: "A", Name: "router.other.net", Value: "203.0.113.10"}, "host-1", Options{})
	if err == nil || !strings.Contains(err.Error(), "outside zone") {
		t.Fatalf("error = %v", err)
	}
}
