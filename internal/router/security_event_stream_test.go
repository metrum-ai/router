// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type failingSecurityEventOutput struct{}

func (failingSecurityEventOutput) Write([]byte) (int, error) {
	return 0, errors.New("fixture write failure")
}
func (failingSecurityEventOutput) Close() error { return nil }

func TestOCSFSecurityEventClassesAndRequiredFields(t *testing.T) {
	cases := []struct {
		eventType string
		classUID  int
		category  int
		className string
	}{
		{"api_auth_failed", 3002, 3, "Authentication"},
		{"admin_auth_failed", 3002, 3, "Authentication"},
		{"admin_auth_throttled", 3002, 3, "Authentication"},
		{"caller_suspended", 3002, 3, "Authentication"},
		{"caller_throttled", 3002, 3, "Authentication"},
		{"detector_signal", 2004, 2, "Detection Finding"},
		{"dlp_hit", 2004, 2, "Detection Finding"},
		{"firewall_block", 2004, 2, "Detection Finding"},
		{"firewall_hold", 2004, 2, "Detection Finding"},
		{"spend_breaker_trip", 2004, 2, "Detection Finding"},
		{"provider_spend_breaker_trip", 2004, 2, "Detection Finding"},
		{"provider_spend_breaker_reset", 2004, 2, "Detection Finding"},
		{"honeytoken_use", 2004, 2, "Detection Finding"},
		{"containment", 6003, 6, "API Activity"},
		{"api_request", 6003, 6, "API Activity"},
	}
	for _, tc := range cases {
		t.Run(tc.eventType, func(t *testing.T) {
			got, err := marshalOCSFSecurityEvent(securityAccessEvent{TS: time.Unix(1, 0), RequestID: "request-1", EventType: tc.eventType, StatusCode: 401, Outcome: "unauthorized"})
			if err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			if err := json.Unmarshal(got, &event); err != nil {
				t.Fatal(err)
			}
			if event["class_uid"] != float64(tc.classUID) || event["category_uid"] != float64(tc.category) || event["class_name"] != tc.className {
				t.Fatalf("class mapping = %#v", event)
			}
			required := []string{"activity_id", "metadata", "severity_id", "status", "status_id", "time", "type_uid", "uid"}
			if tc.classUID == 2004 {
				required = append(required, "finding_info")
			} else if tc.classUID == 3002 {
				required = append(required, "user", "service")
			} else {
				required = append(required, "actor", "api", "src_endpoint", "http_request", "http_response")
			}
			for _, key := range required {
				if _, ok := event[key]; !ok {
					t.Errorf("required field %q missing", key)
				}
			}
			if event["metadata"].(map[string]any)["version"] != "1.4.0" {
				t.Errorf("metadata version = %#v", event["metadata"])
			}
		})
	}
}

func TestSecurityAccessFailuresReachStreamWithoutSecurityReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{securityEventStream: stream}
	svc.recordRequestSecurityAccess(&requestContext{id: "rate-limit-1", method: "POST", pathTemplate: "/v1/chat/completions", caller: &callerRuntime{}}, 429, "rpm-exceeded")
	svc.recordRequestSecurityAccess(&requestContext{id: "suspended-1", method: "POST", pathTemplate: "/v1/chat/completions", caller: &callerRuntime{}}, 403, "key-suspended")
	svc.adminObserveHookFor()(AdminObservation{OperationID: "issueCaller", Method: "POST", Status: 429, RateLimited: true})
	stream.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 3 {
		t.Fatalf("stream emitted %d records, want caller throttle, suspension and admin throttle: %s", len(lines), body)
	}
	joined := string(body)
	if !strings.Contains(joined, "caller_throttled") || !strings.Contains(joined, "caller_suspended") || !strings.Contains(joined, "admin_auth_throttled") {
		t.Fatalf("security events missing from stream: %s", joined)
	}
}

func TestRecordPIISecurityEventContainsNoPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{securityEventStream: stream}
	svc.recordPIISecurityEvent(&requestContext{id: "pii-1", method: "POST", pathTemplate: "/v1/chat/completions"}, 200, "redacted")
	stream.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "email") || !strings.Contains(string(body), `"class_uid":2004`) {
		t.Fatalf("DLP event was missing or included payload: %s", body)
	}
}

func TestContainmentHandlerEmitsSanitizedSuspensionEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{securityEventStream: stream}
	handler := svc.securityContainmentEventHandler(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"caller_id": "private-caller", "status": accountStatusSuspended})
	})
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/admin/v1/containment", nil))
	stream.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "caller_suspended") || strings.Contains(string(body), "private-caller") {
		t.Fatalf("containment event missing or disclosed caller identity: %s", body)
	}
}

func TestSecurityEventWebhookSignsBodyAndRecordsFailure(t *testing.T) {
	secret := []byte(strings.Repeat("k", 32))
	var gotSignature bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(r.Header.Get("X-Router-Timestamp") + "\n"))
		_, _ = mac.Write(body)
		gotSignature = hmac.Equal([]byte(strings.TrimPrefix(r.Header.Get("X-Router-Signature"), "sha256=")), []byte(hex.EncodeToString(mac.Sum(nil))))
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := &securityEventStream{queue: make(chan securityAccessEvent, 1), done: make(chan struct{}), output: securityEventStreamOutput{io.Discard}, client: server.Client(), url: server.URL, secret: secret}
	go s.run()
	s.record(securityAccessEvent{TS: time.Unix(1, 0), RequestID: "webhook-1", EventType: "api_auth_failed", StatusCode: 401})
	close(s.queue)
	<-s.done
	if !gotSignature {
		t.Fatal("webhook signature did not cover timestamp and exact body")
	}
	if s.failed.Load() != 1 {
		t.Fatalf("failed delivery count = %d, want 1", s.failed.Load())
	}
}

func TestSecurityEventWebhookTimeoutIsBounded(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	s := &securityEventStream{queue: make(chan securityAccessEvent, 1), done: make(chan struct{}), output: securityEventStreamOutput{io.Discard}, client: &http.Client{Timeout: 5 * time.Millisecond}, url: server.URL, secret: []byte(strings.Repeat("k", 32))}
	start := time.Now()
	if err := s.deliver([]byte(`{"uid":"1"}`)); err == nil {
		t.Fatal("timed out webhook delivery succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("webhook timeout and retry exceeded one second")
	}
}

func TestSecurityEventStreamWriteFailureAndQueueOverflow(t *testing.T) {
	overflow := &securityEventStream{queue: make(chan securityAccessEvent, 1)}
	overflow.record(securityAccessEvent{EventType: "api_request"})
	overflow.record(securityAccessEvent{EventType: "api_request"})
	if overflow.dropped.Load() != 1 {
		t.Fatalf("dropped count = %d, want one bounded-queue drop", overflow.dropped.Load())
	}
	s := &securityEventStream{queue: make(chan securityAccessEvent, 1), done: make(chan struct{}), output: failingSecurityEventOutput{}}
	go s.run()
	s.record(securityAccessEvent{EventType: "api_request"})
	close(s.queue)
	<-s.done
	if s.failed.Load() != 1 {
		t.Fatalf("failed count = %d, want one output failure", s.failed.Load())
	}
}

func TestServiceStreamsAuthenticationFailureWhenReportsAreOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "security.jsonl")
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.Security.Events = SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "file", Path: path}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[]}`))
	req.Header.Set("Authorization", "Bearer invalid-token")
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	svc.Close()
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("auth status = %d, want 401", response.Code)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"class_uid":3002`) || !strings.Contains(string(body), "api_auth_failed") || strings.Contains(string(body), "invalid-token") {
		t.Fatalf("missing sanitized auth failure event: %s", body)
	}
}

func TestProviderSpendBreakerEventsReachStreamWhenReportsAreOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "security.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{securityEventStream: stream}
	svc.recordProviderSpendEvent("mock", "trip", "daily", 10)
	stream.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"class_uid":2004`) || !strings.Contains(string(body), "provider_spend_breaker_trip") {
		t.Fatalf("missing provider spend breaker detection: %s", body)
	}
}

func TestSecuritySigmaRulesMatchSyntheticTraces(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "security-events")
	tracesRaw, err := os.ReadFile(filepath.Join(dir, "synthetic-traces.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var traces []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(tracesRaw)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		traces = append(traces, event)
	}
	for _, name := range []string{"authentication-failure.yml", "detection-finding.yml", "containment-action.yml"} {
		t.Run(name, func(t *testing.T) {
			var rule struct {
				Detection struct {
					Selection map[string]any `yaml:"selection"`
				} `yaml:"detection"`
			}
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(body, &rule); err != nil {
				t.Fatal(err)
			}
			matched := false
			for _, event := range traces {
				match := true
				for key, want := range rule.Detection.Selection {
					if fmt.Sprint(event[key]) != fmt.Sprint(want) {
						match = false
						break
					}
				}
				matched = matched || match
			}
			if len(rule.Detection.Selection) == 0 || !matched {
				t.Fatalf("rule %s did not match a synthetic trace", name)
			}
		})
	}
}

func TestSecurityEventStreamWritesJSONLFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	s, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	s.record(securityAccessEvent{TS: time.Unix(1, 0), RequestID: "request-1", EventType: "api_auth_failed", StatusCode: 401})
	s.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(strings.TrimSpace(string(body)))) || !strings.HasSuffix(string(body), "\n") {
		t.Fatalf("invalid JSONL event: %q", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("output mode = %o, want 600", info.Mode().Perm())
	}
}

func TestOCSFSecurityEventOmitsPromptAndCredentialFields(t *testing.T) {
	fakeCredential := strings.Join([]string{"sk", "fakecredential12345"}, "-")
	got, err := marshalOCSFSecurityEvent(securityAccessEvent{
		TS: time.Unix(1, 0), RequestID: "request-1", EventType: "api_auth_failed", Outcome: "unauthorized",
		ReasonCode: fakeCredential, AuthSubject: "prompt text must not appear", CallerID: "caller-secret",
		TokenID: "token-secret", RequestedModel: "prompt-secret", CallerUser: "user-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fakeCredential, "prompt text must not appear", "caller-secret", "token-secret", "prompt-secret", "user-secret"} {
		if strings.Contains(string(got), forbidden) {
			t.Errorf("event includes forbidden value %q: %s", forbidden, got)
		}
	}
}

func TestValidateSecurityEvents(t *testing.T) {
	valid := SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "stdout"}
	if err := validateSecurityEvents(valid); err != nil {
		t.Fatalf("valid stream config rejected: %v", err)
	}
	cases := []SecurityEventStreamConfig{
		{Enabled: true, Version: "1.5.0", Output: "stdout"},
		{Enabled: true, Version: "1.4.0", Output: "syslog"},
		{Enabled: true, Version: "1.4.0", Output: "file"},
		{Enabled: true, Version: "1.4.0", Output: "stdout", Path: "/tmp/ignored"},
		{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "http://example.test/events", WebhookSecretEnv: "EVENTS_KEY"},
		{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://example.test/events", WebhookSecretEnv: "not-a-variable"},
	}
	for _, cfg := range cases {
		if err := validateSecurityEvents(cfg); err == nil {
			t.Errorf("invalid config accepted: %#v", cfg)
		}
	}
	if err := validateSecurityEvents(SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://example.test/events", WebhookSecretEnv: "EVENTS_KEY"}); err != nil {
		t.Fatalf("valid webhook config rejected: %v", err)
	}
	if _, err := newSecurityEventStream(SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://example.test/events", WebhookSecretEnv: "EVENTS_KEY"}, func(string) string { return "short" }); err == nil {
		t.Fatal("webhook secret shorter than 32 bytes was accepted")
	}
}
