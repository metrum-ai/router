// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterAddedLatencyTraceHappyPath(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(25 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_latency",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}

	rec := lastJSONLRecord(t, filepath.Join(dir, "requests.jsonl"))
	receive := findTrace(t, rec.TraceEvents, "router_receive")
	wrote := findTrace(t, rec.TraceEvents, "router_upstream_wrote_request")
	if receive.DurationMS != 0 {
		t.Fatalf("router_receive duration_ms=%d want 0", receive.DurationMS)
	}
	if wrote.DurationMS < 0 {
		t.Fatalf("wrote_request duration_ms=%d", wrote.DurationMS)
	}
	// Upstream sleep must not inflate receive-to-WroteRequest.
	if wrote.DurationMS >= 25 {
		t.Fatalf("router-added latency %dms includes upstream wait; want receive-to-send only", wrote.DurationMS)
	}
	if wrote.Message != "router_added_latency receive_to_wrote_request" {
		t.Fatalf("unexpected wrote_request message %q", wrote.Message)
	}
	if !traceEventsContainDB(t, svc, rec.RequestID, "router_receive") {
		t.Fatal("router_receive missing from usage DB")
	}
	if !traceEventsContainDB(t, svc, rec.RequestID, "router_upstream_wrote_request") {
		t.Fatal("router_upstream_wrote_request missing from usage DB")
	}
}

func TestRouterAddedLatencyTraceUpstreamFailureStillRecordsWroteRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream boom"}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatalf("expected upstream failure, got %d body=%s", rr.Code, rr.Body.String())
	}

	rec := lastJSONLRecord(t, filepath.Join(dir, "requests.jsonl"))
	_ = findTrace(t, rec.TraceEvents, "router_receive")
	wrote := findTrace(t, rec.TraceEvents, "router_upstream_wrote_request")
	if wrote.DurationMS < 0 {
		t.Fatalf("wrote_request duration_ms=%d", wrote.DurationMS)
	}
}

func TestRouterReceiveEmittedOnAuthReject(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":8}`))
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	rec := lastJSONLRecord(t, filepath.Join(dir, "requests.jsonl"))
	_ = findTrace(t, rec.TraceEvents, "router_receive")
	_ = findTrace(t, rec.TraceEvents, "auth_rejected")
	for _, event := range rec.TraceEvents {
		if event.Event == "router_upstream_wrote_request" {
			t.Fatal("unexpected wrote_request on auth reject")
		}
	}
}

func lastJSONLRecord(t *testing.T, path string) logRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		t.Fatal("empty request log")
	}
	var rec logRecord
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func findTrace(t *testing.T, events []traceLogRecord, name string) traceLogRecord {
	t.Helper()
	for _, event := range events {
		if event.Event == name {
			return event
		}
	}
	t.Fatalf("missing trace event %q in %#v", name, events)
	return traceLogRecord{}
}

func traceEventsContainDB(t *testing.T, svc *Service, requestID, name string) bool {
	t.Helper()
	var rows []requestTraceEventRecord
	if err := svc.usage.db.Where("request_id = ?", requestID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return traceEventsContain(rows, name)
}
