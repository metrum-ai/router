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
	"testing"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
)

type detectorFunc func(context.Context, detector.Event) ([]detector.Signal, error)

func (f detectorFunc) Detect(ctx context.Context, event detector.Event) ([]detector.Signal, error) {
	return f(ctx, event)
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
	detectorService := httptest.NewServer(detector.Handler(detectorFunc(func(_ context.Context, event detector.Event) ([]detector.Signal, error) {
		events <- event
		return []detector.Signal{}, nil
	})))
	defer detectorService.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, providerKey, dir)
	cfg.Server.Detectors = []DetectorConfig{{ID: "capture", URL: detectorService.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "report", TimeoutMS: 500, OnError: "fallback", SampleRate: 1, IncludeSegments: []string{"tool_result"}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
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
}
