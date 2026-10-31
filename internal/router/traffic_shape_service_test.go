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

func TestTrafficShapeRequestStartRejectsBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_shape_request",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	on := true
	cfg.Callers[0].TrafficShape = TrafficShapeConfig{Enabled: &on, RequestStartPerSec: 0.01, RequestBurst: 1}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"first"}]}`))
	first.Header.Set("Authorization", "Bearer "+testToken)
	firstRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(firstRR, first)
	if firstRR.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRR.Code, firstRR.Body.String())
	}

	second := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"second"}]}`))
	second.Header.Set("Authorization", "Bearer "+testToken)
	secondRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(secondRR, second)
	if secondRR.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d body=%s", secondRR.Code, secondRR.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", calls.Load())
	}
	assertTrafficShapeError(t, secondRR.Body.Bytes(), "caller.request_start_per_sec")
	if secondRR.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing")
	}
}

func TestTrafficShapeQueueDoesNotHoldCallerConcurrency(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_shape_queue",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Rate.Concurrent = 1
	on := true
	cfg.Callers[0].TrafficShape = TrafficShapeConfig{
		Enabled:            &on,
		RequestStartPerSec: 2,
		RequestBurst:       1,
		Queue:              TrafficShapeQueueConfig{Enabled: true, MaxWaitMS: 1000, MaxDepth: 1},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"first"}]}`))
	first.Header.Set("Authorization", "Bearer "+testToken)
	firstRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(firstRR, first)
	if firstRR.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRR.Code, firstRR.Body.String())
	}

	done := make(chan int, 1)
	go func() {
		second := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"second"}]}`))
		second.Header.Set("Authorization", "Bearer "+testToken)
		secondRR := httptest.NewRecorder()
		svc.Handler().ServeHTTP(secondRR, second)
		done <- secondRR.Code
	}()

	deadline := time.Now().Add(250 * time.Millisecond)
	queued := false
	for time.Now().Before(deadline) {
		if len(svc.trafficShape.QueueDepths()) > 0 {
			queued = true
			break
		}
		select {
		case code := <-done:
			t.Fatalf("second request completed before queue observation with status=%d", code)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !queued {
		t.Fatal("second request did not enter the traffic-shaping queue")
	}

	caller, _, err := svc.authenticate("Bearer "+testToken, "")
	if err != nil {
		t.Fatal(err)
	}
	ad := svc.quota.Admit(caller, 0)
	if !ad.OK {
		t.Fatalf("queued shaping request occupied caller concurrency: status=%d reason=%s", ad.Status, ad.Reason)
	}
	svc.quota.Release(caller)

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("second status=%d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second request did not finish after queue refill")
	}
}

func TestTrafficShapeQueueDepthRejectsBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_shape_queue_depth",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	on := true
	cfg.Callers[0].TrafficShape = TrafficShapeConfig{
		Enabled:            &on,
		RequestStartPerSec: 1,
		RequestBurst:       1,
		Queue:              TrafficShapeQueueConfig{Enabled: true, MaxWaitMS: 2000, MaxDepth: 1},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	first := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"first"}]}`))
	first.Header.Set("Authorization", "Bearer "+testToken)
	firstRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(firstRR, first)
	if firstRR.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRR.Code, firstRR.Body.String())
	}

	done := make(chan int, 1)
	go func() {
		second := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"second"}]}`))
		second.Header.Set("Authorization", "Bearer "+testToken)
		secondRR := httptest.NewRecorder()
		svc.Handler().ServeHTTP(secondRR, second)
		done <- secondRR.Code
	}()

	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		if len(svc.trafficShape.QueueDepths()) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second request did not enter the traffic-shaping queue")
		}
		select {
		case code := <-done:
			t.Fatalf("second request completed before queue-depth rejection test with status=%d", code)
		case <-time.After(5 * time.Millisecond):
		}
	}

	third := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"third"}]}`))
	third.Header.Set("Authorization", "Bearer "+testToken)
	thirdRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(thirdRR, third)
	if thirdRR.Code != http.StatusTooManyRequests {
		t.Fatalf("third status=%d body=%s", thirdRR.Code, thirdRR.Body.String())
	}
	assertTrafficShapeError(t, thirdRR.Body.Bytes(), "caller.request_start_per_sec")
	if thirdRR.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing")
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want only first request before queued refill", calls.Load())
	}

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("second status=%d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second request did not finish after queue refill")
	}
}

func TestTrafficShapeInputTokensRejectsBeforeUpstreamAndPersistsTelemetry(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.db"))
	on := true
	cfg.Callers[0].TrafficShape = TrafficShapeConfig{Enabled: &on, InputTokensPerSec: 1, InputTokenBurst: 4}
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":"` + strings.Repeat("x", 100) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls.Load())
	}
	assertTrafficShapeError(t, rr.Body.Bytes(), "caller.input_tokens_per_sec")

	var rows []usageRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("usage rows=%d", len(rows))
	}
	row := rows[0]
	if !row.TrafficShapeApplied || row.TrafficShapeDecision != trafficShapeDecisionRejected ||
		row.TrafficShapeScope != trafficShapeScopeCaller || row.TrafficShapeBucket != "caller.input_tokens_per_sec" ||
		row.TrafficShapeEstimatedInputTokens <= 4 || row.TrafficShapeTotalReservedTokens <= 4 {
		t.Fatalf("traffic shape telemetry missing: %#v", row)
	}
	var events []requestTrafficShapeEventRecord
	flushUsageForTest(t, svc)
	if err := svc.usage.db.Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Bucket != trafficShapeBucketInputTokens || events[0].Decision != trafficShapeDecisionRejected {
		t.Fatalf("traffic shape events missing: %#v", events)
	}

	rawLog, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawLog), testToken) || strings.Contains(string(rawLog), cfg.Callers[0].TokenSHA256) || strings.Contains(string(rawLog), "provider-key") {
		t.Fatalf("traffic shape log leaked secret material: %s", rawLog)
	}
	if !strings.Contains(string(rawLog), `"traffic_shape_decision":"rejected"`) {
		t.Fatalf("traffic shape decision missing from log: %s", rawLog)
	}
}

func TestRecordTrafficShapeResultPreservesQueuedDecision(t *testing.T) {
	rc := &requestContext{}
	recordTrafficShapeResult(rc, trafficShapeResult{
		Applied:     true,
		Decision:    trafficShapeDecisionQueued,
		Scope:       trafficShapeScopeCaller,
		Bucket:      "caller.request_start_per_sec",
		QueueWaitMS: 25,
	})
	recordTrafficShapeResult(rc, trafficShapeResult{
		Applied:              true,
		Decision:             trafficShapeDecisionAdmitted,
		Scope:                trafficShapeScopeCaller,
		Bucket:               "caller.total_reserved_tokens_per_sec",
		EstimatedInputTokens: 10,
		ReservedOutputTokens: 20,
		TotalReservedTokens:  30,
	})
	if rc.rec.TrafficShapeDecision != trafficShapeDecisionQueued {
		t.Fatalf("decision=%q, want queued", rc.rec.TrafficShapeDecision)
	}
	if rc.rec.TrafficShapeBucket != "caller.request_start_per_sec" {
		t.Fatalf("bucket=%q, want request-start bucket", rc.rec.TrafficShapeBucket)
	}
	if rc.rec.TrafficShapeQueueWaitMS != 25 {
		t.Fatalf("queue wait=%d, want 25", rc.rec.TrafficShapeQueueWaitMS)
	}
	if rc.rec.TrafficShapeTotalReservedTokens != 30 {
		t.Fatalf("total reservation=%d, want 30", rc.rec.TrafficShapeTotalReservedTokens)
	}
}

func TestTrafficShapeTotalReservedTokensRejectsHugeOutputCap(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	on := true
	cfg.Callers[0].TrafficShape = TrafficShapeConfig{Enabled: &on, TotalReservedTokensPerSec: 1, TotalReservedTokenBurst: 50}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","max_tokens":500,"messages":[{"role":"user","content":"small"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls.Load())
	}
	assertTrafficShapeError(t, rr.Body.Bytes(), "caller.total_reserved_tokens_per_sec")
}

func assertTrafficShapeError(t *testing.T, raw []byte, bucket string) {
	t.Helper()
	var payload struct {
		Error struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Bucket    string `json:"bucket"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Type != "traffic-shaped" || payload.Error.RequestID == "" || payload.Error.Bucket != bucket {
		t.Fatalf("unexpected traffic-shaped error: %s", raw)
	}
}
