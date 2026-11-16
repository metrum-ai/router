// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestHoneytokenSinkholeProfileVariesReplyAndUsage(t *testing.T) {
	texts := map[string]bool{}
	usages := map[string]bool{}
	for _, id := range []string{"request-a", "request-b", "request-c", "request-d", "request-e", "request-f", "request-g", "request-h"} {
		text, usage := honeytokenSinkholeProfile(id)
		texts[text] = true
		usages[fmt.Sprintf("%d:%d:%d", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)] = true
	}
	if len(texts) < 2 || len(usages) < 2 {
		t.Fatalf("sinkhole profile did not vary: texts=%d usages=%d", len(texts), len(usages))
	}
}

func TestHoneytokenAndCanaryNeverReachUpstream(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "security", Project: "decoys", Environment: "test", KeySlug: "honey",
		Allow: []string{"canary"}, HoneytokenLabel: "docs config example", Now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	streamPath := filepath.Join(t.TempDir(), "security-events.jsonl")
	cfg.Server.Security.Events = SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "file", Path: streamPath}
	cfg.Server.AdminReports.Security.Enabled = false
	cfg.Models["canary"] = ModelGroup{Strategy: "static", Canary: &CanaryGroupConfig{Callers: []string{generated.Caller.ID}}}
	for i := range cfg.Callers {
		cfg.Callers[i].Allow = append(cfg.Callers[i].Allow, "canary")
	}
	cfg.Callers = append(cfg.Callers, generated.Caller)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	unsafeCfg := *cfg
	unsafeCfg.Callers = append([]CallerConfig(nil), cfg.Callers...)
	unsafeCfg.Callers[len(unsafeCfg.Callers)-1].Allow = append(unsafeCfg.Callers[len(unsafeCfg.Callers)-1].Allow, "default")
	if err := unsafeCfg.Validate(); err == nil || !strings.Contains(err.Error(), "only allow canary") {
		t.Fatalf("honeytoken with real group allow must fail validation, got %v", err)
	}
	configBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "router.yaml")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reload honeytoken config: %v", err)
	}
	svc, err := New(loaded)
	if err != nil {
		t.Fatal(err)
	}

	call := func(path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.20:4242"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	decoy := call("/v1/chat/completions", generated.Token, `{"model":"canary","stream":true,"messages":[{"role":"user","content":"secret prompt"}]}`)
	if decoy.Code != http.StatusOK || decoy.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(decoy.Body.String(), honeytokenCompletionText) {
		t.Fatalf("honeytoken status=%d body=%s", decoy.Code, decoy.Body.String())
	}
	canary := call("/v1/chat/completions", generated.Token, `{"model":"canary","messages":[{"role":"user","content":"probe"}]}`)
	if canary.Code != http.StatusOK || !strings.Contains(canary.Body.String(), honeytokenCompletionText) {
		t.Fatalf("canary status=%d body=%s", canary.Code, canary.Body.String())
	}
	responses := call("/v1/responses", generated.Token, `{"model":"canary","input":"secret prompt"}`)
	if responses.Code != http.StatusOK || !strings.Contains(responses.Body.String(), honeytokenCompletionText) {
		t.Fatalf("Responses honeytoken status=%d body=%s", responses.Code, responses.Body.String())
	}
	anthropicReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"canary","max_tokens":32,"messages":[{"role":"user","content":"secret prompt"}]}`))
	anthropicReq.Header.Set("X-API-Key", generated.Token)
	anthropicReq.Header.Set("Anthropic-Version", "2023-06-01")
	anthropicResp := httptest.NewRecorder()
	svc.Handler().ServeHTTP(anthropicResp, anthropicReq)
	if anthropicResp.Code != http.StatusOK || !strings.Contains(anthropicResp.Body.String(), honeytokenCompletionText) {
		t.Fatalf("Anthropic honeytoken status=%d body=%s", anthropicResp.Code, anthropicResp.Body.String())
	}
	notSelected := call("/v1/chat/completions", testToken, `{"model":"canary","messages":[{"role":"user","content":"probe"}]}`)
	if notSelected.Code == http.StatusOK {
		t.Fatalf("non-selected caller used canary: %s", notSelected.Body.String())
	}
	unknown := call("/v1/chat/completions", generated.Token, `{"model":"does-not-exist","messages":[{"role":"user","content":"secret prompt"}]}`)
	if unknown.Code != http.StatusOK || !strings.Contains(unknown.Body.String(), honeytokenCompletionText) {
		t.Fatalf("honeytoken unknown model status=%d body=%s, want canned sinkhole reply", unknown.Code, unknown.Body.String())
	}
	unknownCount := call("/v1/messages/count_tokens", generated.Token, `{"model":"does-not-exist","messages":[{"role":"user","content":"secret prompt"}]}`)
	if unknownCount.Code != http.StatusOK || !strings.Contains(unknownCount.Body.String(), `"input_tokens"`) {
		t.Fatalf("unknown honeytoken count_tokens status=%d body=%s", unknownCount.Code, unknownCount.Body.String())
	}
	disallowed := call("/v1/chat/completions", generated.Token, `{"model":"default","messages":[{"role":"user","content":"secret prompt"}]}`)
	if disallowed.Code != http.StatusOK || !strings.Contains(disallowed.Body.String(), honeytokenCompletionText) {
		t.Fatalf("honeytoken disallowed model status=%d body=%s, want canned sinkhole reply", disallowed.Code, disallowed.Body.String())
	}
	badFeature := call("/v1/responses", generated.Token, `{"model":"canary","input":"secret prompt","reasoning":{"effort":"unsupported"}}`)
	if badFeature.Code != http.StatusBadRequest {
		t.Fatalf("honeytoken unsupported feature status=%d body=%s, want same feature error as a real caller", badFeature.Code, badFeature.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/models", ""},
		{http.MethodGet, "/v1/models/canary", ""},
		{http.MethodGet, "/v1/codex/models.json", ""},
		{http.MethodGet, "/v1/usage", ""},
		{http.MethodPost, "/v1/embeddings", `{"model":"canary","input":"secret prompt"}`},
		{http.MethodPost, "/v1/responses/input_tokens", `{"model":"does-not-exist","input":"secret prompt"}`},
		{http.MethodPost, "/v1/messages/count_tokens", `{"model":"canary","messages":[{"role":"user","content":"secret prompt"}]}`},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+generated.Token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if (tc.path == "/v1/codex/models.json" || tc.path == "/v1/models") && (!strings.Contains(w.Body.String(), `"id":"canary"`) || !strings.Contains(w.Body.String(), `"id":"default"`)) {
			t.Fatalf("model catalog omitted caller's canary group at %s: status=%d body=%s", tc.path, w.Code, w.Body.String())
		}
		if (tc.path == "/v1/embeddings" || tc.path == "/v1/responses/input_tokens") && w.Code != http.StatusOK {
			t.Fatalf("honeytoken local sinkhole %s status=%d body=%s, want 200", tc.path, w.Code, w.Body.String())
		}
		if tc.path == "/v1/messages/count_tokens" && w.Code != http.StatusOK {
			t.Fatalf("canary count_tokens status=%d body=%s, want local estimate 200", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		body       string
		dimensions int
	}{
		{body: `{"model":"canary","input":"secret prompt"}`, dimensions: honeytokenDefaultEmbeddingDimensions},
		{body: `{"model":"canary","input":"secret prompt","dimensions":768}`, dimensions: 768},
	} {
		response := call("/v1/embeddings", generated.Token, tc.body)
		var decoded struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decoded) != nil || len(decoded.Data) != 1 || len(decoded.Data[0].Embedding) != tc.dimensions {
			dimensions := 0
			if len(decoded.Data) > 0 {
				dimensions = len(decoded.Data[0].Embedding)
			}
			t.Fatalf("honeytoken embeddings status=%d vectors=%d dimensions=%d; want one vector with %d dimensions", response.Code, len(decoded.Data), dimensions, tc.dimensions)
		}
	}
	unauthorizedCanary := call("/v1/chat/completions", testToken, `{"model":"canary","messages":[{"role":"user","content":"probe"}]}`)
	if unauthorizedCanary.Code != http.StatusNotFound {
		t.Fatalf("caller outside canary allow list status=%d, want 404", unauthorizedCanary.Code)
	}
	if count := waitForHoneytokenEvents(t, svc, "security_canary_group_used", "canary", 1); count == 0 {
		t.Fatal("rejected canary request did not raise an event")
	}
	for _, tc := range []struct{ path, body string }{
		{"/v1/embeddings", `{"model":"canary","input":"probe"}`},
		{"/v1/messages/count_tokens", `{"model":"canary","messages":[{"role":"user","content":"probe"}]}`},
	} {
		denied := call(tc.path, testToken, tc.body)
		if denied.Code != http.StatusNotFound {
			t.Fatalf("caller outside canary allow list used %s: status=%d body=%s", tc.path, denied.Code, denied.Body.String())
		}
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream calls=%d, want 0", got)
	}
	svc.Close()
	streamBody, err := os.ReadFile(streamPath)
	if err != nil {
		t.Fatal(err)
	}
	streamText := string(streamBody)
	if !strings.Contains(streamText, `"title":"security_honeytoken_used"`) || !strings.Contains(streamText, `"title":"security_canary_group_used"`) {
		t.Fatalf("OCSF stream omitted honeytoken/canary source events: %s", streamText)
	}
	if strings.Contains(streamText, generated.Token) || strings.Contains(streamText, "secret prompt") || strings.Contains(streamText, `"probe"`) {
		t.Fatalf("OCSF stream leaked token or prompt text: %s", streamText)
	}
	store, err := OpenUsageStore(cfg.Server.UsageDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var events []securityAccessEventRecord
	if err := store.db.Where("event_type IN ?", []string{"security_honeytoken_used", "security_canary_group_used"}).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("security events=%+v; want one honeytoken and one canary event per decoy plus one canary event for the rejected real caller", events)
	}
	if svc.honeytokenEventDrops.Load() == 0 {
		t.Fatal("duplicate honeytoken/canary events were not counted")
	}
	assertHoneytokenStreamEvent(t, streamPath, "security_honeytoken_used", generated.Caller.ID, "192.0.2.20", generated.Token)
	assertHoneytokenStreamEvent(t, streamPath, "security_canary_group_used", generated.Caller.ID, "192.0.2.20", generated.Token)
	var honeyEvents, canaryEvents int
	for _, event := range events {
		if event.Outcome != "critical" {
			t.Fatalf("security event outcome=%q, want critical", event.Outcome)
		}
		switch event.EventType {
		case "security_honeytoken_used":
			honeyEvents++
		case "security_canary_group_used":
			canaryEvents++
		}
		encoded, _ := json.Marshal(event)
		if strings.Contains(string(encoded), generated.Token) || strings.Contains(string(encoded), "secret prompt") {
			t.Fatalf("security event leaked token or prompt text: %s", encoded)
		}
	}
	if honeyEvents != 1 || canaryEvents != 2 {
		t.Fatalf("deduplicated event kinds honeytoken=%d canary=%d; want one each", honeyEvents, canaryEvents)
	}
	metricRec := httptest.NewRecorder()
	svc.writeMetricsPayload(metricRec, httptest.NewRequest(http.MethodGet, "/metrics", nil), nil)
	if !strings.Contains(metricRec.Body.String(), "metrum_ai_router_honeytoken_events_deduplicated_total ") {
		t.Fatal("Prometheus scrape did not export the honeytoken event deduplication counter")
	}
	var canaryEventSeen bool
	for _, event := range events {
		canaryEventSeen = canaryEventSeen || event.EventType == "security_canary_group_used"
	}
	if !canaryEventSeen {
		t.Fatalf("canary invocation event missing: %+v", events)
	}

	rows, err := HoneytokenReport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Label != "docs config example" || rows[0].CallerID != generated.Caller.ID || rows[0].PlantedAt == "" || rows[0].LastSeen == "" {
		t.Fatalf("honeytoken report=%+v", rows)
	}
	var canaryLastSeen string
	if err := store.db.Model(&securityAccessEventRecord{}).Select("MAX(ts)").Where("event_type = ? AND caller_id = ?", "security_canary_group_used", generated.Caller.ID).Scan(&canaryLastSeen).Error; err != nil {
		t.Fatal(err)
	}
	if rows[0].LastSeen != canaryLastSeen {
		t.Fatalf("honeytoken report last seen=%q; want latest canary use %q", rows[0].LastSeen, canaryLastSeen)
	}
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Label+row.CallerID+row.LastSeen), "secret") {
			t.Fatalf("report leaked secret-like material: %+v", row)
		}
	}
}

func waitForHoneytokenEvents(t *testing.T, svc *Service, eventType, group string, want int64) int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int64
		query := svc.usage.db.Model(&securityAccessEventRecord{}).Where("event_type = ?", eventType)
		if group != "" {
			query = query.Where("model_group = ?", group)
		}
		if err := query.Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count >= want || time.Now().After(deadline) {
			return count
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func enableHoneytokenStream(t *testing.T, cfg *Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "security-events.jsonl")
	cfg.Server.Security.Events = SecurityEventStreamConfig{Enabled: true, Version: "1.4.0", Output: "file", Path: path}
	return path
}

func assertHoneytokenStreamEvent(t *testing.T, path, eventType, callerID, sourceIP, token string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(body), "\n") {
			var event map[string]any
			if json.Unmarshal([]byte(line), &event) != nil {
				continue
			}
			finding, _ := event["finding_info"].(map[string]any)
			if finding["title"] != eventType {
				continue
			}
			if event["severity_id"] != float64(5) {
				t.Fatalf("%s stream severity_id=%v; want critical (5)", eventType, event["severity_id"])
			}
			evidences, _ := event["evidences"].([]any)
			if len(evidences) != 1 {
				t.Fatalf("%s stream evidence count=%d; want one", eventType, len(evidences))
			}
			evidence, _ := evidences[0].(map[string]any)
			actor, _ := evidence["actor"].(map[string]any)
			user, _ := actor["user"].(map[string]any)
			if user["uid"] != callerID {
				t.Fatalf("%s stream actor caller ID did not match", eventType)
			}
			if _, exists := user["name"]; exists {
				t.Fatalf("%s stream labels the caller with a non-caller name", eventType)
			}
			endpoint, _ := evidence["src_endpoint"].(map[string]any)
			if endpoint["ip"] != sourceIP {
				t.Fatalf("%s stream source IP=%v; want %s", eventType, endpoint["ip"], sourceIP)
			}
			if token != "" && strings.Contains(line, token) {
				t.Fatalf("%s stream contains raw token material", eventType)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s event did not appear in OCSF stream", eventType)
}

func TestHoneytokenUseRecordedWhenAccountPolicyRejects(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{OwnerUser: "security", Project: "decoys", Environment: "test", KeySlug: "suspended", HoneytokenLabel: "suspended test"})
	if err != nil {
		t.Fatal(err)
	}
	generated.Caller.Status = accountStatusSuspended
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "suspended-honeytoken.sqlite"))
	streamPath := enableHoneytokenStream(t, cfg)
	cfg.Callers = append(cfg.Callers, generated.Caller)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "192.0.2.41:4242"
	req.Header.Set("Authorization", "Bearer "+generated.Token)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("suspended honeytoken status=%d body=%s", w.Code, w.Body.String())
	}
	count := waitForHoneytokenEvents(t, svc, "security_honeytoken_used", "", 1)
	if count != 1 {
		t.Fatalf("suspended honeytoken event count=%d err=%v", count, err)
	}
	assertHoneytokenStreamEvent(t, streamPath, "security_honeytoken_used", generated.Caller.ID, "192.0.2.41", generated.Token)
}

func TestHoneytokenUseRecordedWhenCallerBindingRejects(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{OwnerUser: "security", Project: "decoys", Environment: "test", KeySlug: "bound", HoneytokenLabel: "binding test"})
	if err != nil {
		t.Fatal(err)
	}
	generated.Caller.Binding = CallerBindingConfig{Mode: "enforce", AllowedCIDRs: []string{"198.51.100.0/24"}}
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "binding-honeytoken.sqlite"))
	streamPath := enableHoneytokenStream(t, cfg)
	cfg.Callers = append(cfg.Callers, generated.Caller)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "192.0.2.9:1234"
	req.Header.Set("Authorization", "Bearer "+generated.Token)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("binding-rejected honeytoken status=%d body=%s, want 401", w.Code, w.Body.String())
	}
	if count := waitForHoneytokenEvents(t, svc, "security_honeytoken_used", "", 1); count != 1 {
		t.Fatalf("binding-rejected honeytoken event count=%d, want one", count)
	}
	assertHoneytokenStreamEvent(t, streamPath, "security_honeytoken_used", generated.Caller.ID, "192.0.2.9", generated.Token)
}

func TestHoneytokenMCPRequestIsRecordedAndNeverDispatched(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{OwnerUser: "security", Project: "decoys", Environment: "test", KeySlug: "mcp", HoneytokenLabel: "mcp planted key"})
	if err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
	}))
	defer upstream.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "mcp-honeytoken.sqlite"))
	streamPath := enableHoneytokenStream(t, cfg)
	cfg.Callers = append(cfg.Callers, generated.Caller)
	cfg.MCP = MCPGatewayConfig{Enabled: true, Servers: []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}}, Policies: []MCPPolicyConfig{{Project: generated.Caller.Project, Tools: []string{"demo/read"}}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"demo/read","arguments":{}}}`))
	req.RemoteAddr = "192.0.2.43:4242"
	req.Header.Set("Authorization", "Bearer "+generated.Token)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "tool denied by policy") {
		t.Fatalf("honeytoken MCP response status=%d body=%s", w.Code, w.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("honeytoken MCP reached upstream %d times", upstreamCalls.Load())
	}
	count := waitForHoneytokenEvents(t, svc, "security_honeytoken_used", "", 1)
	if count != 1 {
		t.Fatalf("MCP honeytoken event count=%d err=%v", count, err)
	}
	assertHoneytokenStreamEvent(t, streamPath, "security_honeytoken_used", generated.Caller.ID, "192.0.2.43", generated.Token)
}

func TestHoneytokenAdminReportDenialRecordsUse(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{OwnerUser: "security", Project: "decoys", Environment: "test", KeySlug: "report", HoneytokenLabel: "report planted key"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.AdminReports.Enabled = true
	cfg.Server.AdminReports.Security.Enabled = true
	const passwordHashEnv = "ROUTER_ADMIN_BASIC_HONEYTOKEN_TEST"
	t.Setenv(passwordHashEnv, mustBcryptHash(t, "test-password"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{Enabled: true, AllowInsecureHTTP: true, Users: []AdminBasicAuthUser{{Username: "admin", PasswordHashEnv: passwordHashEnv, Subject: "basic:admin", Domain: "local/test"}}}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Source: "static", Policy: []string{"p, basic:admin, local/test, admin:reports, read"}}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "report-honeytoken.sqlite"))
	streamPath := enableHoneytokenStream(t, cfg)
	cfg.Callers = append(cfg.Callers, generated.Caller)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodGet, "/admin/reports", nil)
	req.RemoteAddr = "192.0.2.44:4242"
	req.Header.Set("Authorization", "Bearer "+generated.Token)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("honeytoken admin report status=%d body=%s", w.Code, w.Body.String())
	}
	count := waitForHoneytokenEvents(t, svc, "security_honeytoken_used", "", 1)
	if count != 1 {
		t.Fatalf("admin report honeytoken event count=%d err=%v", count, err)
	}
	assertHoneytokenStreamEvent(t, streamPath, "security_honeytoken_used", generated.Caller.ID, "192.0.2.44", generated.Token)
}

func TestHoneytokenSinkholeSurvivesUnavailableEventStore(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{OwnerUser: "security", Project: "decoys", Environment: "test", KeySlug: "sink", HoneytokenLabel: "event store down"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "down-honeytoken.sqlite"))
	streamPath := enableHoneytokenStream(t, cfg)
	cfg.Callers = append(cfg.Callers, generated.Caller)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	store := svc.usage
	svc.usage = nil // Model the event sink being unavailable.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"unknown-model","messages":[{"role":"user","content":"private"}]}`))
	req.RemoteAddr = "192.0.2.45:4242"
	req.Header.Set("Authorization", "Bearer "+generated.Token)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	svc.usage = store
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), honeytokenCompletionText) {
		t.Fatalf("event-store failure changed sinkhole response status=%d body=%s", w.Code, w.Body.String())
	}
	assertHoneytokenStreamEvent(t, streamPath, "security_honeytoken_used", generated.Caller.ID, "192.0.2.45", generated.Token)
}

func TestHoneytokenEventDedupeIncludesCanaryGroup(t *testing.T) {
	svc := &Service{}
	now := time.Now().UTC()
	if !svc.allowHoneytokenEvent("opaque-token", "security_canary_group_used", now, "canary-a") {
		t.Fatal("first canary group event was suppressed")
	}
	if svc.allowHoneytokenEvent("opaque-token", "security_canary_group_used", now.Add(time.Second), "canary-a") {
		t.Fatal("duplicate event for same token/group was allowed")
	}
	if !svc.allowHoneytokenEvent("opaque-token", "security_canary_group_used", now.Add(time.Second), "canary-b") {
		t.Fatal("different canary group was incorrectly deduplicated")
	}
}

func TestHoneytokenSinkholeUsesDefaultCallerQuota(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "security", Project: "quota-decoys", Environment: "test", KeySlug: "bounded",
		Allow: []string{"canary"}, HoneytokenLabel: "quota test", Now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	generated.Caller.Rate.RPM = 1
	upstreamCalls := atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	cnf := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cnf.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "quota.sqlite"))
	cnf.Models["canary"] = ModelGroup{Strategy: "static", Canary: &CanaryGroupConfig{Callers: []string{generated.Caller.ID}}}
	cnf.Callers = append(cnf.Callers, generated.Caller)
	svc, err := New(cnf)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"canary","messages":[{"role":"user","content":"probe"}]}`))
		req.Header.Set("Authorization", "Bearer "+generated.Token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	if first := call(); first.Code != http.StatusOK {
		t.Fatalf("first sinkhole request status=%d body=%s, want 200", first.Code, first.Body.String())
	}
	if second := call(); second.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit sinkhole request status=%d body=%s, want 429", second.Code, second.Body.String())
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("sinkhole requests reached upstream %d times", calls)
	}
	if svc.honeytokenEventDrops.Load() == 0 {
		t.Fatal("repeated sinkhole request did not increment deduplication counter")
	}
}

func TestHoneytokenEventDedupStateIsBounded(t *testing.T) {
	svc := &Service{}
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10001; i++ {
		if !svc.allowHoneytokenEvent(fmt.Sprintf("%032x", i+1), "security_honeytoken_used", now) {
			t.Fatalf("unique token %d was unexpectedly deduplicated", i)
		}
	}
	if got := len(svc.honeytokenEventLast); got != 10000 {
		t.Fatalf("deduplication state retained %d entries, want 10000", got)
	}
}

func TestAuthenticatedHoneytokenIsRecordedBeforeIdentifiersUnavailable(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "security", Project: "identifier-decoys", Environment: "test", KeySlug: "unavailable",
		Allow: []string{"canary"}, HoneytokenLabel: "identifier test",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "identifiers.sqlite"))
	cfg.Models["canary"] = ModelGroup{Strategy: "static", Canary: &CanaryGroupConfig{Callers: []string{generated.Caller.ID}}}
	cfg.Callers = append(cfg.Callers, generated.Caller)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc.cfg.Server.Identifiers.Mode = "rewrite"
	svc.identifiers = nil
	request := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"canary","messages":[{"role":"user","content":"private"}]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	if got := request(generated.Token).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("authenticated decoy request status=%d, want identifier-unavailable 503", got)
	}
	if got := request("invalid-token").Code; got != http.StatusUnauthorized {
		t.Fatalf("invalid credential status=%d, want auth-first 401", got)
	}
	svc.Close()
	store, err := OpenUsageStore(cfg.Server.UsageDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var honeyEvents int64
	if err := store.db.Model(&securityAccessEventRecord{}).Where("event_type = ? AND caller_id = ?", "security_honeytoken_used", generated.Caller.ID).Count(&honeyEvents).Error; err != nil || honeyEvents != 1 {
		t.Fatalf("authenticated identifier-failure honeytoken events=%d err=%v; want one", honeyEvents, err)
	}
}

func TestDatabaseModeHoneytokenSinkholeEndToEnd(t *testing.T) {
	generated, err := GenerateCallerToken(TokenGenerateOptions{
		OwnerUser: "security", Project: "db-decoys", Environment: "test", KeySlug: "database",
		HoneytokenLabel: "database sinkhole", Now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_HONEYTOKEN_DB_API_KEY", "test-only-db-provider-key")
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	dbPath := filepath.Join(t.TempDir(), "config.sqlite")
	runner, closeConfigDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer closeConfigDB()
	applyConfigControlPlaneMigrationsForTest(t, runner)
	document := fmt.Sprintf(`server:
  identifiers:
    mode: passthrough
  default_model_group: default
providers:
  mock:
    base_url: %q
    dialect: openai
    api_key_env: TEST_HONEYTOKEN_DB_API_KEY
    models:
      small:
        model: mock-small
models:
  default:
    strategy: static
    targets:
      - provider: mock
        model_ref: small
callers:
  - id: %s
    owner_user: security
    project: db-decoys
    environment: test
    status: active
    token_hash: %q
    token_id: %s
    honeytoken:
      label: database sinkhole
      planted_at: "2026-11-16T00:00:00Z"
    allow: []
`, upstream.URL, generated.Caller.ID, generated.Caller.TokenHash, generated.TokenID)
	setID, err := ImportCanonicalYAML(runner.db, "prod", "honeytoken", []byte(document), "test-seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := ActivateConfigSet(runner.db, "prod", setID, "test-seed"); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadActiveConfigFromDB(runner.db, "prod", IdentifierConfig{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Callers) != 1 || loaded.Callers[0].TokenHash != generated.Caller.TokenHash || loaded.Callers[0].TokenID != generated.TokenID {
		t.Fatalf("database projection did not preserve the token verifier and token ID")
	}
	bootstrap, err := LoadActiveCanonicalConfig(runner.db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.Server.ConfigSource = ConfigSourceConfig{Mode: "database", Driver: "sqlite", Path: dbPath, RuntimeScope: "prod"}
	bootstrap.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	svc, err := New(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate stale DB state predating validation: the runtime sinkhole must
	// still prevent a honeytoken from reaching a real group's upstream.
	runtimeDecoy := svc.quota.callers[generated.Caller.ID]
	if runtimeDecoy == nil {
		t.Fatal("database honeytoken runtime missing")
	}
	runtimeDecoy.cfg.Allow = []string{"default"}
	runtimeDecoy.allow["default"] = true
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"private db prompt"}]}`))
	request.Header.Set("Authorization", "Bearer "+generated.Token)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), honeytokenCompletionText) {
		t.Fatalf("database honeytoken with stale real-group grant status=%d body=%s, want local sinkhole response", response.Code, response.Body.String())
	}
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("database honeytoken reached upstream %d times", calls)
	}
	svc.Close()
	store, err := OpenUsageStore(bootstrap.Server.UsageDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var event securityAccessEventRecord
	if err := store.db.Where("event_type = ? AND caller_id = ?", "security_honeytoken_used", generated.Caller.ID).First(&event).Error; err != nil {
		t.Fatalf("database-loaded honeytoken use event missing: %v", err)
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), generated.Token) || strings.Contains(string(encoded), "private db prompt") {
		t.Fatalf("database honeytoken event leaked secret material: %s", encoded)
	}
}
