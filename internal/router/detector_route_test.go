// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	detect "github.com/metrum-ai/router/internal/security/detect"
	"github.com/metrum-ai/router/pkg/detector"
)

type detectorFunc func(context.Context, detector.Event) ([]detector.Signal, error)

func (f detectorFunc) Detect(ctx context.Context, event detector.Event) ([]detector.Signal, error) {
	return f(ctx, event)
}

func TestDetectorRuntimeReplacementAppliesConfigReload(t *testing.T) {
	seen := make(chan detector.Event, 1)
	server := httptest.NewServer(detector.Handler(detectorFunc(func(_ context.Context, event detector.Event) ([]detector.Signal, error) {
		seen <- event
		return []detector.Signal{}, nil
	})))
	defer server.Close()
	svc := &Service{}
	config := []DetectorConfig{{ID: "reload", URL: server.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "report", TimeoutMS: 500, OnError: "fallback", SampleRate: 1}}
	if err := svc.replaceDetectorConfig(config); err != nil {
		t.Fatal(err)
	}
	svc.submitDetectorEvent(detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "request"}, nil, false)
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("reloaded detector did not receive event")
	}
	if err := svc.replaceDetectorConfig(nil); err != nil {
		t.Fatal(err)
	}
	if svc.detectorDispatcher != nil || len(svc.detectors) != 0 {
		t.Fatal("removed detector config remained active")
	}
}

func TestDeniedModelDetectorLabelIsSafeAndOpaque(t *testing.T) {
	raw := "org/model-name"
	label := safeDetectorModelLabel(raw)
	if label == raw || detector.ValidateEvent(detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "model_denied", DeniedGroup: label}) != nil {
		t.Fatalf("unsafe denied-model label %q from %q", label, raw)
	}
}

func TestDeniedNamespacedModelReachesDetectorAsSafeLabel(t *testing.T) {
	seen := make(chan detector.Event, 4)
	detectorService := httptest.NewServer(detector.Handler(detectorFunc(func(_ context.Context, event detector.Event) ([]detector.Signal, error) {
		seen <- event
		return []detector.Signal{}, nil
	})))
	defer detectorService.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Detectors = []DetectorConfig{{ID: "capture", URL: detectorService.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "report", TimeoutMS: 500, OnError: "fallback", SampleRate: 1}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"org/model-name","messages":[{"role":"user","content":"probe"}]}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("namespaced unknown model status=%d, want 404", response.Code)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-seen:
			if event.Kind != "model_denied" {
				continue
			}
			if event.DeniedGroup == "org/model-name" || !strings.HasPrefix(event.DeniedGroup, "model-") {
				t.Fatalf("unsafe denied model detector label: %+v", event)
			}
			return
		case <-deadline:
			t.Fatal("model enumeration event was not delivered")
		}
	}
}

func TestRequestCompletesWhenDetectorQueueIsFull(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"upstream","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	started, gate := make(chan struct{}, 4), make(chan struct{})
	detectorService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate
		_, _ = io.WriteString(w, `[]`)
	}))
	defer detectorService.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.Detectors = []DetectorConfig{{ID: "slow", URL: detectorService.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "report", TimeoutMS: 5000, OnError: "fallback", SampleRate: 1}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	defer close(gate)
	routerServer := httptest.NewServer(svc.mux)
	defer routerServer.Close()
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "request"}
	for i := 0; i < 4; i++ {
		svc.submitDetectorEvent(event, nil, false)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("detector workers did not fill")
		}
	}
	for i := 0; i < 257; i++ {
		svc.submitDetectorEvent(event, nil, false)
	}
	request, _ := http.NewRequest(http.MethodPost, routerServer.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("full detector queue delayed request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if svc.detectorDispatcher.Dropped() == 0 {
		t.Fatal("full detector queue did not increment drop counter")
	}
	dropsBeforeReload := svc.detectorDropped.Load()
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- svc.replaceDetectorConfig(nil) }()
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reload waited for the hung detector")
	}
	if svc.detectorDropped.Load() < dropsBeforeReload+256 {
		t.Fatalf("reload did not count queued jobs as drops: before=%d after=%d", dropsBeforeReload, svc.detectorDropped.Load())
	}
	if err := svc.replaceDetectorConfig(cfg.Server.Detectors); err != nil {
		t.Fatal(err)
	}
	if svc.detectorDropped.Load() < dropsBeforeReload+256 {
		t.Fatal("detector drop counter reset across reload")
	}
	metricsResponse := httptest.NewRecorder()
	svc.writeMetricsPayload(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil), nil)
	if !strings.Contains(metricsResponse.Body.String(), "metrum_ai_router_detector_dropped_total ") {
		t.Fatal("detector drop metric disappeared after removing detector config")
	}
}

func TestDetectorResultConsumerConcurrentWorkers(t *testing.T) {
	svc := &Service{}
	var delivered atomic.Int64
	consumer := func(detect.Result) { delivered.Add(1) }
	svc.SetDetectorResultConsumer(consumer)
	dispatcher := detect.NewDispatcher(4, 512, svc.handleDetectorResult)
	defer dispatcher.Close()
	event := detector.Event{SchemaVersion: detector.SchemaVersion, Kind: "request"}
	policy := detect.Policy{Mode: "enforce", Timeout: time.Second, OnError: "fallback", SampleRate: 1}
	const count = 256
	for i := 0; i < count; i++ {
		if !dispatcher.Submit("fixture", "caller", detectorFunc(func(context.Context, detector.Event) ([]detector.Signal, error) {
			return []detector.Signal{}, nil
		}), event, policy) {
			t.Fatalf("submit %d failed", i)
		}
	}
	stopWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stopWriter:
				return
			default:
				svc.SetDetectorResultConsumer(consumer)
			}
		}
	}()
	deadline := time.After(2 * time.Second)
	for delivered.Load() < count {
		select {
		case <-deadline:
			close(stopWriter)
			<-writerDone
			t.Fatalf("delivered %d/%d results", delivered.Load(), count)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(stopWriter)
	<-writerDone
}

func TestRequestRoutesOptedInSegmentsOffPathAndKeepsPrivateTextOutOfLogs(t *testing.T) {
	const promptMarker = "private prompt marker"
	const segmentMarker = "selected tool result marker"
	const providerKey = "provider-secret-marker"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"upstream","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	events := make(chan detector.Event, 4)
	results := make(chan detect.Result, 4)
	detectorService := httptest.NewServer(detector.Handler(detectorFunc(func(_ context.Context, event detector.Event) ([]detector.Signal, error) {
		events <- event
		index := 0
		return []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "fixture", Severity: "high", Score: 0.75, Key: strings.Repeat("b", 32), EvidenceRequestIDs: []string{event.RequestID, "evidence-prior-2"}, SegmentKind: "tool_result", SegmentIndex: &index}}, nil
	})))
	defer detectorService.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, providerKey, dir)
	enabled := true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &enabled)
	cfg.Server.AdminReports.Security.Enabled = true
	cfg.Server.Detectors = []DetectorConfig{{ID: "capture", URL: detectorService.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "report", TimeoutMS: 500, OnError: "fallback", SampleRate: 1, IncludeSegments: []string{"tool_result"}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.SetDetectorResultConsumer(func(result detect.Result) { results <- result })
	server := httptest.NewServer(svc.mux)
	defer server.Close()
	body := `{"model":"default","messages":[{"role":"user","content":"` + promptMarker + `"},{"role":"tool","tool_call_id":"call_1","content":"` + segmentMarker + `"}]}`
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("request status=%d body=%s", resp.StatusCode, responseBody)
	}
	var requestEvent detector.Event
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for requestEvent.Kind != "request" {
		select {
		case requestEvent = <-events:
		case <-timer.C:
			t.Fatal("request event not submitted")
		}
	}
	if requestEvent.Kind != "request" || len(requestEvent.Segments) != 1 || requestEvent.Segments[0].Kind != "tool_result" || requestEvent.Segments[0].Text != segmentMarker {
		t.Fatalf("request event=%+v", requestEvent)
	}
	wire, _ := json.Marshal(requestEvent)
	if strings.Contains(string(wire), promptMarker) || strings.Contains(string(wire), testToken) || strings.Contains(string(wire), providerKey) {
		t.Fatal("event carried prompt or credential text")
	}
	sum := sha256.Sum256([]byte("rtr_alice_test"))
	if requestEvent.KeyID != hex.EncodeToString(sum[:16]) || requestEvent.KeyID == "rtr_alice_test" || requestEvent.KeyID == testToken {
		t.Fatal("event key identifier was not the opaque hash of the caller token id")
	}
	logBytes, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logBytes), promptMarker) || strings.Contains(string(logBytes), segmentMarker) || strings.Contains(string(logBytes), testToken) || strings.Contains(string(logBytes), providerKey) {
		t.Fatal("request log carried prompt, detector segment, or credential text")
	}
	var result detect.Result
	select {
	case result = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("detector result was not delivered")
	}
	resultBytes, _ := json.Marshal(result)
	if strings.Contains(string(resultBytes), promptMarker) || strings.Contains(string(resultBytes), segmentMarker) || strings.Contains(string(resultBytes), testToken) || strings.Contains(string(resultBytes), providerKey) {
		t.Fatal("detector result carried prompt, segment, or credential text")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var records []securityAccessEventRecord
		if err := svc.usage.db.Where("event_type = ?", "security_detector_signal").Find(&records).Error; err != nil {
			t.Fatal(err)
		}
		if len(records) > 0 {
			if records[0].RequestedModel != "fixture" || !strings.Contains(records[0].ReasonCode, "fixture:high") || !strings.Contains(records[0].ReasonCode, "evidence=") || !strings.Contains(records[0].ReasonCode, requestEvent.RequestID) || !strings.Contains(records[0].ReasonCode, "evidence-prior-2") {
				t.Fatalf("security event omitted detector rule or evidence IDs: %+v", records[0])
			}
			recordBytes, _ := json.Marshal(records)
			if strings.Contains(string(recordBytes), promptMarker) || strings.Contains(string(recordBytes), segmentMarker) || strings.Contains(string(recordBytes), testToken) || strings.Contains(string(recordBytes), providerKey) {
				t.Fatal("security event carried prompt, segment, or credential text")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detector signal was not stored on security event path")
}
