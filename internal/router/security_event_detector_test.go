// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
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

func TestDetectorSignalsReachOCSFStream(t *testing.T) {
	const promptMarker = "private detector stream prompt"
	const providerKey = "provider-secret-marker"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"upstream","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	detected := make(chan detector.Event, 4)
	detectorService := httptest.NewServer(detector.Handler(detectorFunc(func(_ context.Context, event detector.Event) ([]detector.Signal, error) {
		detected <- event
		return []detector.Signal{{SchemaVersion: detector.SchemaVersion, DetectorID: "honeytoken", Severity: "high", Score: 0.75, EvidenceRequestIDs: []string{event.RequestID, "prior-request-2"}}}, nil
	})))
	defer detectorService.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "security-events.jsonl")
	cfg := testConfig(t, upstream.URL, providerKey, dir)
	usageEnabled := true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &usageEnabled)
	cfg.Server.AdminReports.Security.Enabled = true
	cfg.Server.Security.Events = SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "file", Path: path}
	cfg.Server.Detectors = []DetectorConfig{{ID: "capture", URL: detectorService.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "report", TimeoutMS: 500, OnError: "fallback", SampleRate: 1}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.mux)
	defer server.Close()
	body := `{"model":"default","messages":[{"role":"user","content":"` + promptMarker + `"}]}`
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
	select {
	case <-detected:
	case <-time.After(2 * time.Second):
		t.Fatal("detector did not receive the completed request")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		wire, err := os.ReadFile(path)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(wire)), "\n") {
				var event struct {
					ClassUID    int            `json:"class_uid"`
					UID         string         `json:"uid"`
					FindingInfo map[string]any `json:"finding_info"`
				}
				if json.Unmarshal([]byte(line), &event) != nil || event.ClassUID != 2004 || event.FindingInfo["title"] != "honeytoken" {
					continue
				}
				description, _ := event.FindingInfo["desc"].(string)
				if event.UID == "" || !strings.Contains(description, "honeytoken:high") || !strings.Contains(description, "0.7500") || !strings.Contains(description, "evidence=") || !strings.Contains(description, "prior-request-2") {
					t.Fatalf("detector OCSF finding missing evidence or signal details: %+v", event)
				}
				if strings.Contains(line, promptMarker) || strings.Contains(line, testToken) || strings.Contains(line, providerKey) {
					t.Fatal("detector OCSF finding carried prompt or credential text")
				}
				flushCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				if err := svc.usage.writer.flush(flushCtx); err != nil {
					cancel()
					t.Fatal(err)
				}
				cancel()
				var records []securityAccessEventRecord
				if err := svc.usage.db.Where("event_type = ?", "security_detector_signal").Find(&records).Error; err != nil {
					t.Fatal(err)
				}
				if len(records) == 0 || records[0].RequestedModel != "honeytoken" {
					t.Fatalf("security report did not use configured detector ID: %#v", records)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	wire, _ := os.ReadFile(path)
	t.Fatalf("detector signal did not reach the OCSF event stream: %s", wire)
}
