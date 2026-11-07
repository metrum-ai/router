// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDigitalOceanProviderCalls(t *testing.T) {
	var stored []map[string]any
	api := newFakeAPI(t, func(req requestLog, w http.ResponseWriter) {
		switch {
		case req.method == http.MethodGet && req.path == "/domains":
			writeJSON(t, w, 200, map[string]any{
				"domains": []map[string]any{{"name": "example.com"}},
				"links":   map[string]any{"pages": map[string]any{}},
			})
		case req.method == http.MethodGet && req.path == "/domains/example.com/records":
			writeJSON(t, w, 200, map[string]any{
				"domain_records": stored,
				"links":          map[string]any{"pages": map[string]any{}},
			})
		case req.method == http.MethodPost && req.path == "/domains/example.com/records":
			var body map[string]any
			if err := json.Unmarshal([]byte(req.body), &body); err != nil {
				t.Errorf("create body is not JSON: %v", err)
			}
			body["id"] = 42
			stored = append(stored, body)
			writeJSON(t, w, 201, map[string]any{"domain_record": body})
		case req.method == http.MethodPut && req.path == "/domains/example.com/records/42":
			var body map[string]any
			if err := json.Unmarshal([]byte(req.body), &body); err != nil {
				t.Errorf("update body is not JSON: %v", err)
			}
			body["id"] = 42
			stored = []map[string]any{body}
			writeJSON(t, w, 200, map[string]any{"domain_record": body})
		case req.method == http.MethodDelete && req.path == "/domains/example.com/records/42":
			stored = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s", req)
			writeJSON(t, w, 404, map[string]any{"id": "not_found", "message": "not found"})
		}
	})
	provider, err := NewDigitalOcean("do-token", Options{Endpoint: api.server.URL})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	ctx := context.Background()

	zones, err := provider.Zones(ctx)
	if err != nil || len(zones) != 1 || zones[0] != "example.com" {
		t.Fatalf("zones = %v, %v", zones, err)
	}

	id, err := provider.Create(ctx, "example.com", Record{Type: "TXT", Name: "_acme-challenge.example.com", Value: "token-value", TTL: 60})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "42" {
		t.Fatalf("create id = %q, want 42", id)
	}
	writes := api.writes()
	if len(writes) != 1 {
		t.Fatalf("create made %d writes", len(writes))
	}
	if got, want := writes[0].body, `{"data":"token-value","name":"_acme-challenge","ttl":60,"type":"TXT"}`; got != want {
		t.Fatalf("create body = %s, want %s", got, want)
	}
	if got := writes[0].header.Get("Authorization"); got != "Bearer do-token" {
		t.Fatalf("Authorization = %q", got)
	}

	records, err := provider.Records(ctx, "example.com")
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(records) != 1 || records[0].Name != "_acme-challenge.example.com" {
		t.Fatalf("records = %+v (relative names must be expanded to FQDNs)", records)
	}

	if err := provider.Update(ctx, "example.com", "42", Record{Type: "A", Name: "router.example.com", Value: "203.0.113.10", TTL: 300}); err != nil {
		t.Fatalf("update: %v", err)
	}
	updateRequests := api.writes()
	if got, want := updateRequests[len(updateRequests)-1].body, `{"data":"203.0.113.10","name":"router","ttl":300,"type":"A"}`; got != want {
		t.Fatalf("update body = %s, want %s", got, want)
	}
	if err := provider.Delete(ctx, "example.com", "42"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestDigitalOceanProviderErrors(t *testing.T) {
	api := newFakeAPI(t, func(req requestLog, w http.ResponseWriter) {
		writeJSON(t, w, 401, map[string]any{"id": "unauthorized", "message": "Unable to authenticate you"})
	})
	provider, err := NewDigitalOcean("do-token", Options{Endpoint: api.server.URL})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	_, err = provider.Zones(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Unable to authenticate you") {
		t.Fatalf("error = %v", err)
	}
	if _, err := NewDigitalOcean("", Options{}); err == nil {
		t.Fatal("expected an error without a token")
	}
}
