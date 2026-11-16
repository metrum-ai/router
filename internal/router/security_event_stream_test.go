// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestOCSFSecurityEventClassMapping(t *testing.T) {
	cases := []struct {
		eventType string
		classUID  int
		category  int
		className string
		activity  int
	}{
		{"api_auth_failed", 3002, 3, "Authentication", 1},
		{"admin_auth_failed", 3002, 3, "Authentication", 1},
		{"admin_authz_failed", 3002, 3, "Authentication", 1},
		{"admin_auth_throttled", 3002, 3, "Authentication", 1},
		{"caller_suspended", 3001, 3, "Account Change", 5},
		{"caller_released", 3001, 3, "Account Change", 2},
		{"caller_throttled", 3002, 3, "Authentication", 1},
		{"detector_signal", 2004, 2, "Detection Finding", 1},
		{"security_detector_signal", 2004, 2, "Detection Finding", 1},
		{"security_detector_failure", 2004, 2, "Detection Finding", 1},
		{"dlp_hit", 2004, 2, "Detection Finding", 1},
		{"firewall_block", 2004, 2, "Detection Finding", 1},
		{"firewall_hold", 2004, 2, "Detection Finding", 1},
		{"caller_quota_exhausted", 2004, 2, "Detection Finding", 1},
		{"spend_breaker_trip", 2004, 2, "Detection Finding", 1},
		{"provider_spend_breaker_trip", 2004, 2, "Detection Finding", 1},
		{"provider_spend_breaker_reset", 2004, 2, "Detection Finding", 1},
		{"honeytoken_use", 2004, 2, "Detection Finding", 1},
		{"containment", 6003, 6, "API Activity", 1},
		{"api_request", 6003, 6, "API Activity", 1},
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
			if event["class_uid"] != float64(tc.classUID) || event["category_uid"] != float64(tc.category) || event["class_name"] != tc.className || event["activity_id"] != float64(tc.activity) {
				t.Fatalf("class mapping = %#v", event)
			}
			if event["metadata"].(map[string]any)["version"] != "1.4.0" {
				t.Errorf("metadata version = %#v", event["metadata"])
			}
		})
	}
}

func TestOCSFSecurityEventsValidateAgainstPinned140Classes(t *testing.T) {
	root := filepath.Join("testdata", "ocsf-1.4.0", "events")
	var classes []struct {
		UID        int                       `json:"uid"`
		Attributes map[string]ocsfSchemaAttr `json:"attributes"`
	}
	var base map[string]ocsfSchemaAttr
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var schema struct {
			UID        int                        `json:"uid"`
			Attributes map[string]json.RawMessage `json:"attributes"`
		}
		if err := json.Unmarshal(body, &schema); err != nil {
			return err
		}
		attrs := map[string]ocsfSchemaAttr{}
		for name, raw := range schema.Attributes {
			var attr ocsfSchemaAttr
			if json.Unmarshal(raw, &attr) == nil && len(raw) > 0 && raw[0] == '{' {
				attrs[name] = attr
			}
		}
		if filepath.Base(path) == "base_event.json" {
			base = attrs
		} else {
			classes = append(classes, struct {
				UID        int                       `json:"uid"`
				Attributes map[string]ocsfSchemaAttr `json:"attributes"`
			}{schema.UID, attrs})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(classes) != 4 || len(base) == 0 {
		t.Fatalf("pinned OCSF class definitions incomplete: %d classes, base attrs=%d", len(classes), len(base))
	}
	for _, source := range []securityAccessEvent{
		{TS: time.Unix(1, 0), RequestID: "auth-1", EventType: "api_auth_failed", StatusCode: 401, CallerID: "caller-1", IPAddress: "192.0.2.1"},
		{TS: time.Unix(2, 0), RequestID: "api-1", EventType: "caller_suspended", StatusCode: 403, CallerID: "caller-1", AdminSubject: "adminkey:secops", IPAddress: "192.0.2.1"},
		{TS: time.Unix(3, 0), RequestID: strings.Repeat("x", 300), EventType: "security_detector_signal", StatusCode: 200, RequestedModel: "configured-id"},
		{TS: time.Unix(4, 0), RequestID: "finding-1", EventType: "dlp_hit", StatusCode: 400},
		{TS: time.Unix(5, 0), RequestID: "activity-1", EventType: "api_request", StatusCode: 200, IPAddress: "192.0.2.1"},
		{TS: time.Unix(6, 0), RequestID: "approval-1", EventType: "firewall_hold", StatusCode: 200, Outcome: "pending", ReasonCode: "approval_pending:approval_0123456789abcdef0123456789abcdef"},
		{TS: time.Unix(7, 0), RequestID: "approval-2", EventType: "firewall_block", StatusCode: 200, Outcome: "denied", ReasonCode: "approval_denied:approval_0123456789abcdef0123456789abcdef"},
	} {
		line, err := marshalOCSFSecurityEvent(source)
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		classUID, ok := event["class_uid"].(float64)
		if !ok {
			t.Fatalf("class_uid missing from %s", line)
		}
		if source.EventType == "security_detector_signal" {
			if event["uid"] != strings.Repeat("x", 256) || event["finding_info"].(map[string]any)["uid"] != strings.Repeat("x", 256) {
				t.Errorf("external evidence request ID was not capped in finding UIDs: %s", line)
			}
		}
		if strings.HasPrefix(source.EventType, "firewall_") && !isSecurityStreamEvent(source.EventType) {
			t.Errorf("firewall approval event %q was filtered from OCSF stream", source.EventType)
		}
		categoryUID, ok := event["category_uid"].(float64)
		if !ok {
			t.Fatalf("category_uid missing from %s", line)
		}
		var class *struct {
			UID        int                       `json:"uid"`
			Attributes map[string]ocsfSchemaAttr `json:"attributes"`
		}
		for i := range classes {
			if int(classUID)/1000 == int(categoryUID) && int(classUID)%1000 == classes[i].UID {
				class = &classes[i]
				break
			}
		}
		if class == nil {
			t.Fatalf("event class %v/%v does not match pinned OCSF 1.4.0 definitions", classUID, categoryUID)
		}
		attrs := make(map[string]ocsfSchemaAttr, len(base)+len(class.Attributes))
		for name, attr := range base {
			attrs[name] = attr
		}
		for name, attr := range class.Attributes {
			attrs[name] = attr
		}
		for name, attr := range attrs {
			if attr.Requirement == "required" {
				if _, exists := event[name]; !exists {
					t.Errorf("%s: required OCSF field %q missing (schema class %d)", source.EventType, name, int(classUID))
				}
			}
			if value, exists := event[name]; exists && len(attr.Enum) != 0 && name != "class_uid" && name != "category_uid" && (name != "activity_id" || len(class.Attributes["activity_id"].Enum) != 0) {
				if _, allowed := attr.Enum[fmt.Sprint(value)]; !allowed {
					t.Errorf("%s: OCSF field %q value %v is absent from pinned enum", source.EventType, name, value)
				}
			}
		}
	}
}

type ocsfSchemaAttr struct {
	Requirement string                     `json:"requirement"`
	Enum        map[string]json.RawMessage `json:"enum"`
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

func TestAdminSecurityAccessDoesNotStoreOperationInModelColumns(t *testing.T) {
	dir := t.TempDir()
	usageEnabled := true
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &usageEnabled)
	cfg.Server.AdminReports.Security.Enabled = true
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/config/sets", nil)
	svc.recordAdminSecurityAccess(request, adminAuthSubject{subject: "operator-1", domain: "admin"}, http.StatusOK, "allowed", "admin:config", "write")
	flushCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := svc.usage.writer.flush(flushCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	var records []securityAccessEventRecord
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := svc.usage.db.Where("event_type = ?", "admin_access").Find(&records).Error; err != nil {
			t.Fatal(err)
		}
		if len(records) != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(records) != 1 {
		t.Fatalf("admin report rows = %d, want 1", len(records))
	}
	if records[0].ModelGroup != "" || records[0].RequestedModel != "" || records[0].ResolvedGroup != "" {
		t.Fatalf("admin operation was stored in model columns: %+v", records[0])
	}
	if records[0].AdminSubject != "operator-1" || records[0].AdminDomain != "admin" {
		t.Fatalf("admin identity fields missing from dedicated columns: %+v", records[0])
	}
}

func TestAdminAuthorizationRefusalReachesSecurityStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: &Config{}, securityEventStream: stream}
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/config/sets", nil)
	svc.recordAdminSecurityAccess(request, adminAuthSubject{subject: "operator-1", domain: "admin"}, http.StatusForbidden, "admin-forbidden", "admin:config", "write")
	stream.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "admin_authz_failed") || !strings.Contains(string(body), `"class_uid":3002`) {
		t.Fatalf("admin authorization refusal missing from security stream: %s", body)
	}
}

func TestAdminV1DenialSinkStreamsPrincipalAndSourceIP(t *testing.T) {
	dir := t.TempDir()
	usageEnabled := true
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &usageEnabled)
	cfg.Server.AdminReports.Security.Enabled = true
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	path := filepath.Join(dir, "events.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	svc.securityEventStream = stream
	err = svc.adminDenialSink().RecordDenial(context.Background(), AdminDenialRecord{
		At: time.Now().UTC(), PrincipalKind: "api-key", PrincipalSubject: "admin-key-1", RequestID: "denial-1",
		Status: http.StatusForbidden, Method: http.MethodPost, Path: "/admin/v1/config/sets",
		ReasonCode: "admin-forbidden", SourceIP: "192.0.2.55",
	})
	if err != nil {
		t.Fatal(err)
	}
	stream.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "admin_authz_failed") || !strings.Contains(string(body), "admin-key-1") || !strings.Contains(string(body), "192.0.2.55") {
		t.Fatalf("admin denial event lost type, principal, or source IP: %s", body)
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
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/containment", nil)
	request = request.WithContext(WithPrincipal(request.Context(), Principal{Subject: "adminkey:secops", Kind: "adminkey"}))
	handler(response, request)
	stream.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(body), &event); err != nil {
		t.Fatal(err)
	}
	user := event["user"].(map[string]any)
	actor := event["actor"].(map[string]any)["user"].(map[string]any)
	if event["class_uid"] != float64(3001) || event["activity_id"] != float64(5) || event["message"] != "caller_suspended allowed" || user["uid"] != "private-caller" || actor["uid"] != "adminkey:secops" {
		t.Fatalf("containment event omitted the target or actor identity: %s", body)
	}
	if strings.Contains(string(body), "token") || strings.Contains(string(body), "secret") {
		t.Fatalf("containment event disclosed a token or secret: %s", body)
	}
}

func TestSecurityEventWebhookSignsBodyAndRecordsFailure(t *testing.T) {
	secret := []byte(strings.Repeat("k", 32))
	var gotSignature bool
	var gotNonce string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotNonce = r.Header.Get("X-Router-Nonce")
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(r.Header.Get("X-Router-Timestamp") + "\n" + gotNonce + "\n"))
		_, _ = mac.Write(body)
		gotSignature = hmac.Equal([]byte(strings.TrimPrefix(r.Header.Get("X-Router-Signature"), "sha256=")), []byte(hex.EncodeToString(mac.Sum(nil))))
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := &securityEventStream{queue: make(chan securityAccessEvent, 1), webhookQueue: make(chan []byte, 1), done: make(chan struct{}), output: securityEventStreamOutput{io.Discard}, client: server.Client(), url: server.URL, secret: secret}
	go s.run()
	s.webhookDone = make(chan struct{})
	go s.runWebhook()
	s.record(securityAccessEvent{TS: time.Unix(1, 0), RequestID: "webhook-1", EventType: "api_auth_failed", StatusCode: 401})
	s.close()
	if !gotSignature || gotNonce == "" {
		t.Fatal("webhook signature did not cover timestamp, nonce, and exact body")
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

func TestSecurityEventStreamDoesNotQueueOrdinaryAccessTraffic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{securityEventStream: stream}
	svc.recordSecurityAccessEvent(securityAccessEvent{RequestID: "allowed-api", EventType: "api_request", StatusCode: http.StatusOK})
	svc.recordSecurityAccessEvent(securityAccessEvent{RequestID: "allowed-admin", EventType: "admin_access", StatusCode: http.StatusOK})
	svc.recordSecurityAccessEvent(securityAccessEvent{RequestID: "auth-failure", EventType: "api_auth_failed", StatusCode: http.StatusUnauthorized})
	svc.recordSecurityAccessEvent(securityAccessEvent{RequestID: "approval-pending", EventType: "firewall_hold", StatusCode: http.StatusOK})
	svc.recordSecurityAccessEvent(securityAccessEvent{RequestID: "approval-denied", EventType: "firewall_block", StatusCode: http.StatusOK})
	stream.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "allowed-api") || strings.Contains(string(body), "allowed-admin") || !strings.Contains(string(body), "auth-failure") || !strings.Contains(string(body), "approval-pending") || !strings.Contains(string(body), "approval-denied") {
		t.Fatalf("security stream included ordinary traffic or omitted auth failure: %s", body)
	}
}

func TestSecurityEventWebhookQueueDropDoesNotBlockSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	s := &securityEventStream{queue: make(chan securityAccessEvent, 4), webhookQueue: make(chan []byte, 1), done: make(chan struct{}), output: f, outputPath: path}
	s.webhookQueue <- []byte("occupy worker queue")
	go s.run()
	s.record(securityAccessEvent{RequestID: "sink-keeps-going", EventType: "api_auth_failed", StatusCode: 401})
	s.close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "sink-keeps-going") || s.webhookDropped.Load() != 1 {
		t.Fatalf("slow webhook affected sink output or drop counter: body=%s dropped=%d", body, s.webhookDropped.Load())
	}
}

func TestSlowWebhookDoesNotDelayFileSink(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	releaseWebhook := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	t.Cleanup(releaseWebhook)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECURITY_EVENTS_TEST_KEY", strings.Repeat("e", 32))
	path := filepath.Join(t.TempDir(), "events.jsonl")
	stream, err := newSecurityEventStreamWithClient(SecurityEventStreamConfig{
		Output: "file", Path: path, WebhookURL: server.URL, WebhookAllowHosts: []string{parsed.Hostname()}, WebhookSecretEnv: "SECURITY_EVENTS_TEST_KEY",
	}, os.Getenv, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	stream.record(securityAccessEvent{RequestID: "sink-before-webhook-response", EventType: "api_auth_failed", StatusCode: 401})
	select {
	case <-started:
	case <-time.After(time.Second):
		stream.close()
		t.Fatal("webhook worker did not start")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(path)
		if strings.Contains(string(body), "sink-before-webhook-response") {
			releaseWebhook()
			stream.close()
			return
		}
		time.Sleep(time.Millisecond)
	}
	releaseWebhook()
	stream.close()
	t.Fatal("file sink waited for the webhook response")
}

func TestSecurityEventFileReopensAfterLogrotateRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	stream.record(securityAccessEvent{RequestID: "before-rotate", EventType: "api_auth_failed", StatusCode: 401})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(path)
		if strings.Contains(string(body), "before-rotate") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	before, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(before), "before-rotate") {
		stream.close()
		t.Fatalf("first event did not reach file before logrotate rename: %s (%v)", before, err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	stream.record(securityAccessEvent{RequestID: "after-rotate", EventType: "api_auth_failed", StatusCode: 401})
	stream.close()
	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rotated), "before-rotate") || !strings.Contains(string(active), "after-rotate") {
		t.Fatalf("rotation did not reopen active file; rotated=%s active=%s", rotated, active)
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

func TestServiceWebhookReceivesSignedAuthenticationEvent(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	received := make(chan bool, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			received <- false
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mac := hmac.New(sha256.New, secret)
		nonce := r.Header.Get("X-Router-Nonce")
		_, _ = mac.Write([]byte(r.Header.Get("X-Router-Timestamp") + "\n" + nonce + "\n"))
		_, _ = mac.Write(body)
		provided, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Router-Signature"), "sha256="))
		var event map[string]any
		valid := nonce != "" && err == nil && hmac.Equal(provided, mac.Sum(nil)) && json.Unmarshal(body, &event) == nil && event["class_uid"] == float64(3002) && event["message"] == "api_auth_failed unauthorized"
		received <- valid
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("SECURITY_EVENTS_WEBHOOK_TEST_KEY", string(secret))
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newSecurityEventStreamWithClient(SecurityEventStreamConfig{
		Output: "file", Path: filepath.Join(dir, "events.jsonl"), WebhookURL: server.URL,
		WebhookAllowHosts: []string{parsed.Hostname()}, WebhookSecretEnv: "SECURITY_EVENTS_WEBHOOK_TEST_KEY",
	}, os.Getenv, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	svc.securityEventStream = stream
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[]}`))
	req.Header.Set("Authorization", "Bearer invalid-token")
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("auth status = %d, want 401", response.Code)
	}
	select {
	case valid := <-received:
		if !valid {
			t.Fatal("configured webhook did not receive the signed OCSF auth event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for configured webhook event")
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
	var traces []map[string]any
	for _, input := range []securityAccessEvent{
		{TS: time.Unix(1, 0), RequestID: "auth-failure", EventType: "api_auth_failed", StatusCode: http.StatusUnauthorized},
		{TS: time.Unix(2, 0), RequestID: "detection-finding", EventType: "dlp_hit", StatusCode: http.StatusBadRequest},
		{TS: time.Unix(3, 0), RequestID: "containment-suspend", EventType: "caller_suspended", StatusCode: http.StatusOK, Outcome: "allowed"},
		{TS: time.Unix(4, 0), RequestID: "containment-resume", EventType: "caller_released", StatusCode: http.StatusOK, Outcome: "allowed"},
		{TS: time.Unix(5, 0), RequestID: "unrelated-api", EventType: "api_request", StatusCode: http.StatusOK},
		{TS: time.Unix(6, 0), RequestID: "suspended-refusal", EventType: "caller_suspended", StatusCode: http.StatusForbidden, Outcome: "forbidden"},
	} {
		line, err := marshalOCSFSecurityEvent(input)
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		traces = append(traces, event)
	}
	negativeAuthFailure, err := marshalOCSFSecurityEvent(securityAccessEvent{TS: time.Unix(7, 0), RequestID: "auth-negative", EventType: "api_auth_failed", StatusCode: http.StatusUnauthorized})
	if err != nil {
		t.Fatal(err)
	}
	var negative map[string]any
	if err := json.Unmarshal(negativeAuthFailure, &negative); err != nil {
		t.Fatal(err)
	}
	traces = append(traces, negative)
	for _, name := range []string{"authentication-failure.yml", "detection-finding.yml", "containment-action.yml"} {
		t.Run(name, func(t *testing.T) {
			var rule struct {
				Detection struct {
					Condition string `yaml:"condition"`
				} `yaml:"detection"`
			}
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(body, &rule); err != nil {
				t.Fatal(err)
			}
			var raw struct {
				Detection map[string]any `yaml:"detection"`
			}
			if err := yaml.Unmarshal(body, &raw); err != nil {
				t.Fatal(err)
			}
			selections := map[string]map[string]any{}
			for key, value := range raw.Detection {
				if key == "condition" {
					continue
				}
				encoded, _ := yaml.Marshal(value)
				var selection map[string]any
				if err := yaml.Unmarshal(encoded, &selection); err != nil {
					t.Fatal(err)
				}
				selections[key] = selection
			}
			matched := map[string]bool{}
			for _, event := range traces {
				uid, _ := event["uid"].(string)
				if sigmaConditionMatches(rule.Detection.Condition, selections, event) {
					matched[uid] = true
				}
			}
			if len(selections) == 0 || len(matched) == 0 {
				t.Fatalf("rule %s did not match a synthetic trace", name)
			}
			if name == "containment-action.yml" {
				for _, uid := range []string{"containment-suspend", "containment-resume"} {
					if !matched[uid] {
						t.Errorf("containment rule did not match positive %s trace", uid)
					}
				}
				for _, uid := range []string{"auth-negative", "unrelated-api", "suspended-refusal"} {
					if matched[uid] {
						t.Errorf("containment rule matched negative %s trace", uid)
					}
				}
			}
		})
	}
}

func sigmaConditionMatches(condition string, selections map[string]map[string]any, event map[string]any) bool {
	matchesSelection := func(selection map[string]any) bool {
		if len(selection) == 0 {
			return false
		}
		for key, want := range selection {
			if fmt.Sprint(event[key]) != fmt.Sprint(want) {
				return false
			}
		}
		return true
	}
	for _, conjunction := range strings.Split(strings.TrimSpace(condition), " or ") {
		matchedAll := true
		for _, selectionName := range strings.Split(strings.TrimSpace(conjunction), " and ") {
			selection, ok := selections[strings.TrimSpace(selectionName)]
			if !ok || !matchesSelection(selection) {
				matchedAll = false
				break
			}
		}
		if matchedAll {
			return true
		}
	}
	return false
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

func TestSecurityEventStreamWritesJSONLStdout(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = writer.Close()
		_ = reader.Close()
	})
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "stdout"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	stream.record(securityAccessEvent{TS: time.Unix(1, 0), RequestID: "stdout-1", EventType: "api_auth_failed", StatusCode: http.StatusUnauthorized})
	stream.close()
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(strings.TrimSpace(string(body)))) || !strings.Contains(string(body), `"uid":"stdout-1"`) {
		t.Fatalf("stdout did not receive JSONL security event: %q", body)
	}
}

func TestOCSFSecurityEventOmitsPromptAndCredentialFields(t *testing.T) {
	fakeCredential := strings.Join([]string{"sk", "fakecredential12345"}, "-")
	got, err := marshalOCSFSecurityEvent(securityAccessEvent{
		TS: time.Unix(1, 0), RequestID: "request-1", EventType: "api_auth_failed", Outcome: "unauthorized",
		ReasonCode: fakeCredential, AuthSubject: "prompt text must not appear", CallerID: "caller-secret", IPAddress: "192.0.2.4",
		TokenID: "token-secret", RequestedModel: "prompt-secret", CallerUser: "user-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fakeCredential, "prompt text must not appear", "token-secret", "prompt-secret", "user-secret"} {
		if strings.Contains(string(got), forbidden) {
			t.Errorf("event includes forbidden value %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(string(got), `"uid":"caller-secret"`) || !strings.Contains(string(got), `"ip":"192.0.2.4"`) {
		t.Fatalf("auth event omitted caller correlation fields: %s", got)
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
		{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://example.test/events", WebhookSecretEnv: "EVENTS_KEY"},
		{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://example.test/events", WebhookAllowHosts: []string{"other.example"}, WebhookSecretEnv: "EVENTS_KEY"},
		{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://127.0.0.1/events", WebhookAllowHosts: []string{"127.0.0.1"}, WebhookSecretEnv: "EVENTS_KEY"},
	}
	for _, cfg := range cases {
		if err := validateSecurityEvents(cfg); err == nil {
			t.Errorf("invalid config accepted: %#v", cfg)
		}
	}
	if err := validateSecurityEvents(SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://example.test/events", WebhookAllowHosts: []string{"example.test"}, WebhookSecretEnv: "EVENTS_KEY"}); err != nil {
		t.Fatalf("valid webhook config rejected: %v", err)
	}
	if _, err := newSecurityEventStream(SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://example.test/events", WebhookSecretEnv: "EVENTS_KEY"}, func(string) string { return "short" }); err == nil {
		t.Fatal("webhook secret shorter than 32 bytes was accepted")
	}
}

func TestSecurityEventWebhookUsesRestrictedEgressAndNoRedirects(t *testing.T) {
	secret := strings.Repeat("w", 32)
	s, err := newSecurityEventStream(SecurityEventStreamConfig{
		Enabled: true, Version: "1.4.0", Output: "stdout", WebhookURL: "https://siem.example/events",
		WebhookAllowHosts: []string{"siem.example"}, WebhookSecretEnv: "EVENTS_KEY",
	}, func(string) string { return secret })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.client.Transport.(*http.Transport); !ok {
		t.Fatalf("webhook transport = %T, want egress transport", s.client.Transport)
	}
	if s.client.CheckRedirect == nil {
		t.Fatal("webhook client has no redirect policy")
	}
	if err := s.client.CheckRedirect(&http.Request{URL: &url.URL{Scheme: "https", Host: "siem.example"}}, []*http.Request{{}}); err != http.ErrUseLastResponse {
		t.Fatalf("redirect policy error = %v, want ErrUseLastResponse", err)
	}
	s.close()
}
