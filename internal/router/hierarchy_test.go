// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metrum-ai/router/pkg/detector"
	"gopkg.in/yaml.v3"
)

func TestHierarchyParentPublishesCallerBoundPolicy(t *testing.T) {
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	events := make(chan detector.Event, 4)
	detectorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event detector.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err == nil {
			events <- event
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"signals":[]}`))
	}))
	defer detectorServer.Close()
	cfg.Server.Detectors = []DetectorConfig{{ID: "hierarchy-test", URL: detectorServer.URL + "/v1/detect", AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true, Mode: "report", TimeoutMS: 1000}}
	cfg.Callers[0].Binding = CallerBindingConfig{Mode: "enforce", AllowedCIDRs: []string{"192.0.2.0/24"}}
	addTokenHashTestCaller(t, cfg)
	cfg.Server.Hierarchy.Parent.Children = []HierarchyChildPolicyConfig{{
		ID: "team-a", CallerID: cfg.Callers[0].ID, OrganizationID: "org-a", TeamID: "team-a",
		AllowedGroups: []string{"default"}, ToolAllowList: []string{"read"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	request := httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.RemoteAddr = "192.0.2.1:1234"
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", response.Code, response.Body.String())
	}
	var policy HierarchyPolicy
	if err := json.Unmarshal(response.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Version != 1 || policy.ChildID != "team-a" || policy.OrganizationID != "org-a" || policy.TeamID != "team-a" || policy.Budget.Month.Tokens != cfg.Callers[0].Quota.Month.Tokens || len(policy.AllowedGroups) != 1 || policy.AllowedGroups[0] != "default" || len(policy.ToolAllowList) != 1 || policy.ToolAllowList[0] != "read" {
		t.Fatalf("unexpected policy: %#v", policy)
	}
	if strings.Contains(response.Body.String(), testToken) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("policy response leaked a credential or is cacheable: headers=%v body=%s", response.Header(), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	request.Header.Set("Authorization", "Bearer invalid-token")
	response = httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), cfg.Callers[0].ID) {
		t.Fatalf("unauthorized policy status=%d body=%s", response.Code, response.Body.String())
	}
	deadline := time.After(time.Second)
	seenAuthFailure := false
	for {
		select {
		case event := <-events:
			if event.Kind == "auth_failure" {
				seenAuthFailure = true
			}
		case <-deadline:
			if !seenAuthFailure {
				t.Fatal("policy endpoint did not emit auth_failure")
			}
			break
		}
		if seenAuthFailure {
			break
		}
	}
	request = httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	request.Header.Set("Authorization", "Bearer "+tokenHashTestToken)
	response = httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("authenticated but unmapped caller got policy: status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.RemoteAddr = "198.51.100.2:1234"
	response = httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("policy endpoint ignored caller network binding: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHierarchyMiddleRouterPolicyEndpointRefusesUnmappedCaller(t *testing.T) {
	t.Setenv("HIERARCHY_MIDDLE_CREDENTIAL", "middle-test-credential")
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	addTokenHashTestCaller(t, cfg)
	cfg.Server.Hierarchy = HierarchyConfig{
		Parent: HierarchyParentConfig{Children: []HierarchyChildPolicyConfig{{
			ID: "team-a", CallerID: "alice", OrganizationID: "org-a", TeamID: "team-a", AllowedGroups: []string{"default"},
		}}},
		Child: HierarchyChildConfig{ID: "middle", ParentURL: "https://parent.example.test", CredentialEnv: "HIERARCHY_MIDDLE_CREDENTIAL", CacheTTL: "30s"},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	request.Header.Set("Authorization", "Bearer "+tokenHashTestToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unmapped middle-router caller got policy: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHierarchyCallerMappingAcceptsSafeDetectorID(t *testing.T) {
	hierarchy := HierarchyConfig{Parent: HierarchyParentConfig{Children: []HierarchyChildPolicyConfig{{
		ID: "team-a", CallerID: "Team_A.bot", OrganizationID: "org-a", TeamID: "team-a",
	}}}}
	if err := validateHierarchyConfig(hierarchy); err != nil {
		t.Fatalf("safe caller ID was rejected: %v", err)
	}
	if err := validateHierarchyCallers(hierarchy, []CallerConfig{{ID: "Team_A.bot", Status: "active"}}); err != nil {
		t.Fatalf("safe caller mapping was rejected: %v", err)
	}
}

func TestHierarchyPolicyEndpointIsAbsentWhenDisabled(t *testing.T) {
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("disabled hierarchy endpoint status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHierarchyPolicyNarrowsResponsesAndCountTokenGroups(t *testing.T) {
	t.Setenv("HIERARCHY_COUNT_CREDENTIAL", "count-test-credential")
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"input_tokens": 1})
	}))
	defer provider.Close()
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		policy := HierarchyPolicy{Version: 1, ChildID: "app", OrganizationID: "org-a", TeamID: "team-a", AllowedGroups: []string{"default"}, PIIFilter: PIIFilterConfig{Enabled: true, Mode: "fail_on_match", ApplyTo: PIIFilterApplyTo{Messages: boolPtr(true)}, Rules: []PIIFilterRule{{Name: "parent-email", Expression: `@example[.]test`, PlaceholderPrefix: "EMAIL"}}}}
		_ = json.NewEncoder(w).Encode(policy)
	}))
	defer parent.Close()
	cfg := testConfig(t, provider.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "restricted")
	cfg.Models["restricted"] = cfg.Models["default"]
	responsesProvider := cfg.Provider["mock"]
	responsesProvider.Dialect = "openai-responses"
	cfg.Provider["responses"] = responsesProvider
	defaultGroup := cfg.Models["default"]
	defaultGroup.Targets = append(defaultGroup.Targets, Target{Provider: "responses", Model: "mock-model", Weight: 1})
	cfg.Models["default"] = defaultGroup
	cfg.Models["restricted"] = defaultGroup
	cfg.Server.Hierarchy.Child = HierarchyChildConfig{ID: "app", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_COUNT_CREDENTIAL", CacheTTL: "30s"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	modelList := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelList.Header.Set("Authorization", "Bearer "+testToken)
	modelListResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(modelListResponse, modelList)
	if modelListResponse.Code != http.StatusOK || !strings.Contains(modelListResponse.Body.String(), `"id":"default"`) || strings.Contains(modelListResponse.Body.String(), `"id":"restricted"`) {
		t.Fatalf("inherited model catalog status=%d body=%s", modelListResponse.Code, modelListResponse.Body.String())
	}
	restrictedModel := httptest.NewRequest(http.MethodGet, "/v1/models/restricted", nil)
	restrictedModel.Header.Set("Authorization", "Bearer "+testToken)
	restrictedModelResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(restrictedModelResponse, restrictedModel)
	if restrictedModelResponse.Code != http.StatusNotFound || !strings.Contains(restrictedModelResponse.Body.String(), "model_not_found") {
		t.Fatalf("inherited model detail status=%d body=%s", restrictedModelResponse.Code, restrictedModelResponse.Body.String())
	}
	for _, tc := range []struct {
		path, body string
		status     int
		detail     string
	}{
		{"/v1/responses/input_tokens", `{"model":"restricted","input":"hello"}`, http.StatusNotFound, "model_not_found"},
		{"/v1/messages/count_tokens", `{"model":"restricted","messages":[{"role":"user","content":"hello"}]}`, http.StatusNotFound, "Model not found."},
		{"/v1/responses/input_tokens", `{"model":"default","input":"alice@example.test"}`, http.StatusBadRequest, "pii-filter-blocked"},
		{"/v1/messages/count_tokens", `{"model":"default","messages":[{"role":"user","content":"alice@example.test"}]}`, http.StatusBadRequest, "pii-filter-blocked"},
	} {
		request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		request.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, request)
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.detail) {
			t.Errorf("%s inherited-policy response status=%d body=%s, want status=%d detail=%q", tc.path, response.Code, response.Body.String(), tc.status, tc.detail)
		}
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("inherited group denials reached provider %d times", providerCalls.Load())
	}
}

func TestHierarchyMCPFiltersToolsAndBlocksInheritedPII(t *testing.T) {
	t.Setenv("HIERARCHY_MCP_CREDENTIAL", "mcp-policy-test-credential")
	t.Setenv("ROUTER_MCP_HIERARCHY_UPSTREAM", "mcp-upstream-test-credential")
	var toolCalls atomic.Int32
	mcpUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "initialize":
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": mcpVersionLegacy, "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "test", "version": "1"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{map[string]any{"name": "read"}, map[string]any{"name": "write"}}}})
		case "tools/call":
			toolCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{}}})
		default:
			http.Error(w, "unexpected MCP method", http.StatusBadRequest)
		}
	}))
	defer mcpUpstream.Close()
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		policy := HierarchyPolicy{Version: 1, ChildID: "app", OrganizationID: "org-a", TeamID: "team-a", AllowedGroups: []string{"default"}, ToolAllowList: []string{"demo/read"}, PIIFilter: PIIFilterConfig{Enabled: true, Mode: "fail_on_match", ApplyTo: PIIFilterApplyTo{Messages: boolPtr(true)}, Rules: []PIIFilterRule{{Name: "parent-email", Expression: `@example[.]test`, PlaceholderPrefix: "EMAIL"}}}}
		_ = json.NewEncoder(w).Encode(policy)
	}))
	defer parent.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "hierarchy-mcp-usage.db"))
	cfg.MCP = MCPGatewayConfig{Enabled: true, Path: "/mcp", Servers: []MCPServerConfig{{Name: "demo", URL: mcpUpstream.URL, CredentialEnv: "ROUTER_MCP_HIERARCHY_UPSTREAM", AllowHosts: []string{"127.0.0.1"}}}, Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read", "demo/write"}, DenyByDefault: true}}}
	cfg.Server.Hierarchy.Child = HierarchyChildConfig{ID: "app", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_MCP_CREDENTIAL", CacheTTL: "30s"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	call := func(method, name, argument string) *httptest.ResponseRecorder {
		params := `{"name":"` + name + `","arguments":{"text":"` + argument + `"}}`
		if method == "tools/list" {
			params = `{}`
		}
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
		request.Header.Set("Authorization", "Bearer "+testToken)
		request.Header.Set("X-Router-Model-Group", "default")
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, request)
		return response
	}
	listed := call("tools/list", "", "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), `"name":"demo/write"`) || !strings.Contains(listed.Body.String(), `"name":"demo/read"`) {
		t.Fatalf("MCP inherited tool list=%d %s", listed.Code, listed.Body.String())
	}
	denied := call("tools/call", "demo/write", "ok")
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), "tool denied") || toolCalls.Load() != 0 {
		t.Fatalf("MCP inherited tool denial=%d calls=%d body=%s", denied.Code, toolCalls.Load(), denied.Body.String())
	}
	pii := call("tools/call", "demo/read", "alice@example.test")
	if pii.Code != http.StatusOK || !strings.Contains(pii.Body.String(), "PII policy") || toolCalls.Load() != 0 {
		t.Fatalf("MCP inherited PII denial=%d calls=%d body=%s", pii.Code, toolCalls.Load(), pii.Body.String())
	}
}

func TestHierarchyConfigurationLoadsFromYAML(t *testing.T) {
	t.Setenv("HIERARCHY_CONFIG_CREDENTIAL", "config-test-credential")
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	cfg.Server.Hierarchy = HierarchyConfig{
		Parent: HierarchyParentConfig{Children: []HierarchyChildPolicyConfig{{
			ID: "team-a", CallerID: "alice", OrganizationID: "org-a", TeamID: "team-a", AllowedGroups: []string{"default"}, ToolAllowList: []string{"read"},
		}}},
		Child: HierarchyChildConfig{ID: "app-a", ParentURL: "https://router.example.com", CredentialEnv: "HIERARCHY_CONFIG_CREDENTIAL", CacheTTL: "45s", TimeoutMS: 1500},
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "router.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Server.Hierarchy; got.Child.ID != "app-a" || got.Child.CredentialEnv != "HIERARCHY_CONFIG_CREDENTIAL" || got.Child.CacheTTL != "45s" || got.Parent.Children[0].OrganizationID != "org-a" || got.Parent.Children[0].ToolAllowList[0] != "read" {
		t.Fatalf("loaded hierarchy config=%#v", got)
	}
}

func TestHierarchyPolicyRuntimeCacheTTLAndFailure(t *testing.T) {
	const token = "hierarchy-test-credential"
	t.Setenv("HIERARCHY_TEST_CREDENTIAL", token)
	var calls atomic.Int32
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != hierarchyPolicyPath || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "invalid request", http.StatusUnauthorized)
			return
		}
		if unavailable.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(HierarchyPolicy{Version: 1, ChildID: "team-a", OrganizationID: "org-a", TeamID: "team-a", AllowedGroups: []string{"default"}, ToolAllowList: []string{}})
	}))
	defer server.Close()
	runtime, err := newHierarchyPolicyRuntime(HierarchyChildConfig{ID: "team-a", ParentURL: server.URL, CredentialEnv: "HIERARCHY_TEST_CREDENTIAL", CacheTTL: "30s"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	policy, err := runtime.refresh(context.Background(), now)
	if err != nil || policy.ChildID != "team-a" || policy.ToolAllowList == nil {
		t.Fatalf("initial fetch policy=%#v err=%v", policy, err)
	}
	unavailable.Store(true)
	if cached, ok := runtime.current(now.Add(29 * time.Second)); !ok || cached.OrganizationID != "org-a" {
		t.Fatalf("fresh cached policy unavailable: %#v, ok=%t", cached, ok)
	}
	if _, err := runtime.refresh(context.Background(), now.Add(29*time.Second)); err != nil {
		t.Fatalf("policy should remain cached within TTL: %v", err)
	}
	if _, ok := runtime.current(now.Add(31 * time.Second)); ok {
		t.Fatal("expired policy remained usable")
	}
	if stale, err := runtime.refresh(context.Background(), now.Add(31*time.Second)); err != nil || stale.OrganizationID != "org-a" {
		t.Fatalf("parent outage should use stale policy during grace: policy=%#v err=%v", stale, err)
	}
	if stale, err := runtime.refresh(context.Background(), now.Add(32*time.Second)); err != nil || stale.OrganizationID != "org-a" {
		t.Fatalf("cached failure should keep stale policy available: policy=%#v err=%v", stale, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("failure cooldown caused another parent call: %d", got)
	}
	if _, err := runtime.refresh(context.Background(), now.Add(61*time.Second)); err == nil {
		t.Fatal("expired stale policy remained usable after grace")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("parent calls=%d, want initial fetch and two post-expiry attempts", got)
	}
}

func TestHierarchyPolicyRefreshUsesSingleflight(t *testing.T) {
	t.Setenv("HIERARCHY_SINGLEFLIGHT_CREDENTIAL", "singleflight-test-credential")
	var calls atomic.Int32
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(40 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(HierarchyPolicy{Version: 1, ChildID: "team-a", OrganizationID: "org-a", TeamID: "team-a"})
	}))
	defer parent.Close()
	runtime, err := newHierarchyPolicyRuntime(HierarchyChildConfig{ID: "team-a", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_SINGLEFLIGHT_CREDENTIAL", CacheTTL: "30s"})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 12
	var wg sync.WaitGroup
	errCh := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policy, err := runtime.refresh(context.Background(), time.Now())
			if err == nil && policy.ChildID != "team-a" {
				err = fmt.Errorf("unexpected policy child %q", policy.ChildID)
			}
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent refreshes made %d parent requests, want 1", got)
	}
}

func TestHierarchyPolicyRefreshSurvivesCanceledFirstRequester(t *testing.T) {
	t.Setenv("HIERARCHY_CANCEL_CREDENTIAL", "cancel-test-credential")
	started, release := make(chan struct{}), make(chan struct{})
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(HierarchyPolicy{Version: 1, ChildID: "team-a", OrganizationID: "org-a", TeamID: "team-a"})
	}))
	defer parent.Close()
	runtime, err := newHierarchyPolicyRuntime(HierarchyChildConfig{ID: "team-a", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_CANCEL_CREDENTIAL", CacheTTL: "30s"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := runtime.refresh(ctx, time.Now()); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first requester error=%v, want canceled", err)
	}
	second := make(chan error, 1)
	go func() {
		policy, err := runtime.refresh(context.Background(), time.Now())
		if err == nil && policy.ChildID != "team-a" {
			err = fmt.Errorf("unexpected policy child %q", policy.ChildID)
		}
		second <- err
	}()
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("shared refresh failed after first requester canceled: %v", err)
	}
	if policy, err := runtime.refresh(context.Background(), time.Now()); err != nil || policy.ChildID != "team-a" {
		t.Fatalf("successful shared fetch was not cached: policy=%#v err=%v", policy, err)
	}
}

func TestHierarchyPolicyDepthRejectedBeforeRefresh(t *testing.T) {
	t.Setenv("HIERARCHY_CANCEL_CREDENTIAL", "cancel-test-credential")
	var calls atomic.Int32
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer parent.Close()
	runtime, err := newHierarchyPolicyRuntime(HierarchyChildConfig{ID: "team-a", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_CANCEL_CREDENTIAL", CacheTTL: "30s"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), hierarchyDepthContextKey{}, hierarchyPolicyMaxDepth)
	if _, err := runtime.refresh(ctx, time.Now()); err == nil || err.Error() != "hierarchy policy depth exceeded" {
		t.Fatalf("depth result err=%v", err)
	}
	if calls.Load() != 0 || !runtime.failedAt.IsZero() {
		t.Fatalf("depth rejection joined/fetched or cached a failure: calls=%d failed_at=%v", calls.Load(), runtime.failedAt)
	}
}

func TestHierarchyPolicyRuntimeTimeoutFailsClosed(t *testing.T) {
	t.Setenv("HIERARCHY_TIMEOUT_CREDENTIAL", "timeout-test-credential")
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(HierarchyPolicy{Version: 1, ChildID: "team-a", OrganizationID: "org-a", TeamID: "team-a"})
	}))
	defer parent.Close()
	runtime, err := newHierarchyPolicyRuntime(HierarchyChildConfig{ID: "team-a", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_TIMEOUT_CREDENTIAL", CacheTTL: "30s", TimeoutMS: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.refresh(context.Background(), time.Now()); err == nil {
		t.Fatal("timed out policy fetch unexpectedly succeeded")
	}
}

func TestHierarchyParentTimeoutFailsInferenceClosed(t *testing.T) {
	t.Setenv("HIERARCHY_INFERENCE_CREDENTIAL", "timeout-test-credential")
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer parent.Close()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Hierarchy.Child = HierarchyChildConfig{ID: "team-a", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_INFERENCE_CREDENTIAL", CacheTTL: "1m", TimeoutMS: 5}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || upstreamCalls.Load() != 0 || strings.Contains(response.Body.String(), "timeout-test-credential") {
		t.Fatalf("parent timeout did not fail closed safely: status=%d upstream_calls=%d body=%s", response.Code, upstreamCalls.Load(), response.Body.String())
	}
}

func TestHierarchyChildServesFromCacheWhileParentPolicyIsDown(t *testing.T) {
	t.Setenv("HIERARCHY_CACHE_OUTAGE_CREDENTIAL", testToken)
	var policyDown atomic.Bool
	var policyCalls atomic.Int32
	var inferenceCalls atomic.Int32
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case hierarchyPolicyPath:
			policyCalls.Add(1)
			if policyDown.Load() {
				http.Error(w, "policy unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(HierarchyPolicy{
				Version: 1, ChildID: "team-a", OrganizationID: "org-a", TeamID: "team-a",
				AllowedGroups: []string{"default"}, ToolAllowList: []string{},
			})
		case "/v1/chat/completions":
			inferenceCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer parent.Close()

	cfg := testConfig(t, parent.URL, "", t.TempDir())
	cfg.Callers[0].Quota.Month.Tokens = 8
	provider := cfg.Provider["mock"]
	provider.APIKey = testToken
	provider.UpstreamKind = "router"
	cfg.Provider["mock"] = provider
	cfg.Server.Hierarchy.Child = HierarchyChildConfig{
		ID: "team-a", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_CACHE_OUTAGE_CREDENTIAL", CacheTTL: "1m",
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for i := 0; i < 2; i++ {
		if i == 1 {
			policyDown.Store(true)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
		request.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "quota-exhausted") {
		t.Fatalf("local child quota status=%d body=%s", response.Code, response.Body.String())
	}
	if got := policyCalls.Load(); got != 1 {
		t.Fatalf("policy calls=%d, want one cached snapshot", got)
	}
	if got := inferenceCalls.Load(); got != 2 {
		t.Fatalf("inference calls=%d, want two parent requests", got)
	}
}

func TestHierarchyChildRefusesParentDisallowedModelGroup(t *testing.T) {
	t.Setenv("HIERARCHY_GROUP_POLICY_CREDENTIAL", testToken)
	var inferenceCalls atomic.Int32
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == hierarchyPolicyPath {
			_ = json.NewEncoder(w).Encode(HierarchyPolicy{
				Version: 1, ChildID: "team-a", OrganizationID: "org-a", TeamID: "team-a",
				AllowedGroups: []string{}, ToolAllowList: []string{},
			})
			return
		}
		inferenceCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer parent.Close()
	cfg := testConfig(t, parent.URL, "", t.TempDir())
	provider := cfg.Provider["mock"]
	provider.APIKey = testToken
	provider.UpstreamKind = "router"
	cfg.Provider["mock"] = provider
	cfg.Server.Hierarchy.Child = HierarchyChildConfig{
		ID: "team-a", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_GROUP_POLICY_CREDENTIAL", CacheTTL: "1m",
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || inferenceCalls.Load() != 0 {
		t.Fatalf("parent-disallowed group status=%d inference_calls=%d body=%s", response.Code, inferenceCalls.Load(), response.Body.String())
	}
}

func TestHierarchyChildReportsParentModelGroupMismatch(t *testing.T) {
	t.Setenv("HIERARCHY_GROUP_MISMATCH_CREDENTIAL", testToken)
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == hierarchyPolicyPath {
			_ = json.NewEncoder(w).Encode(HierarchyPolicy{
				Version: 1, ChildID: "team-a", OrganizationID: "org-a", TeamID: "team-a",
				AllowedGroups: []string{"gpt-5.1"}, ToolAllowList: []string{},
			})
			return
		}
		t.Errorf("mismatched model group reached inference endpoint")
	}))
	defer parent.Close()
	cfg := testConfig(t, parent.URL, "", t.TempDir())
	provider := cfg.Provider["mock"]
	provider.APIKey = testToken
	provider.UpstreamKind = "router"
	cfg.Provider["mock"] = provider
	cfg.Server.Hierarchy.Child = HierarchyChildConfig{
		ID: "team-a", ParentURL: parent.URL, CredentialEnv: "HIERARCHY_GROUP_MISMATCH_CREDENTIAL", CacheTTL: "1m",
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "hierarchy-model-group-mismatch") {
		t.Fatalf("group mismatch status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHierarchyPolicyRuntimeChangesThroughAtomicPublication(t *testing.T) {
	t.Setenv("HIERARCHY_RELOAD_CREDENTIAL", "reload-test-credential")
	parentA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(HierarchyPolicy{Version: 1, ChildID: "team-a", OrganizationID: "org-a", TeamID: "team-a"})
	}))
	defer parentA.Close()
	parentB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(HierarchyPolicy{Version: 1, ChildID: "team-b", OrganizationID: "org-a", TeamID: "team-b"})
	}))
	defer parentB.Close()
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	cfg.Server.Hierarchy.Child = HierarchyChildConfig{ID: "team-a", ParentURL: parentA.URL, CredentialEnv: "HIERARCHY_RELOAD_CREDENTIAL", CacheTTL: "1m"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	first, err := svc.currentHierarchyPolicy(context.Background())
	if err != nil || first.ChildID != "team-a" {
		t.Fatalf("initial hierarchy policy=%#v err=%v", first, err)
	}
	next, err := newHierarchyPolicyRuntime(HierarchyChildConfig{ID: "team-b", ParentURL: parentB.URL, CredentialEnv: "HIERARCHY_RELOAD_CREDENTIAL", CacheTTL: "1m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.publishMachinePlane(func(machinePlaneSnapshot) (*machinePlanePublication, error) {
		return &machinePlanePublication{HierarchyPolicy: next, SetHierarchyPolicy: true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := svc.currentHierarchyPolicy(context.Background())
	if err != nil || updated.ChildID != "team-b" {
		t.Fatalf("reloaded hierarchy policy=%#v err=%v", updated, err)
	}
}

func TestHierarchyPolicyNarrowingAndPIIUnion(t *testing.T) {
	policy := &HierarchyPolicy{AllowedGroups: []string{"safe"}, ToolAllowList: []string{"read"}}
	if !hierarchyPolicyAllowsGroup(policy, "safe") || hierarchyPolicyAllowsGroup(policy, "admin") {
		t.Fatal("model group policy was not enforced")
	}
	if !hierarchyPolicyAllowsTools(policy, []map[string]any{{"name": "read"}}) || hierarchyPolicyAllowsTools(policy, []map[string]any{{"name": "write"}}) {
		t.Fatal("tool allow-list was not enforced")
	}
	if !hierarchyPolicyAllowsTools(policy, []map[string]any{{"function": map[string]any{"name": "read"}}}) || hierarchyPolicyAllowsTools(policy, []map[string]any{{"function": map[string]any{"name": "write"}}}) {
		t.Fatal("OpenAI function tool policy was not enforced")
	}
	parent := PIIFilterConfig{Enabled: true, SecretsEnabled: true, Mode: "redact_only", ApplyTo: PIIFilterApplyTo{Messages: boolPtr(true)}, Rules: []PIIFilterRule{{Name: "parent_email", Expression: `@example\\.test`, PlaceholderPrefix: "PARENT_EMAIL"}}}
	child := PIIFilterConfig{Enabled: true, Mode: "fail_on_match", ApplyTo: PIIFilterApplyTo{System: boolPtr(true)}, Rules: []PIIFilterRule{{Name: "email", Expression: `@example\\.test`, PlaceholderPrefix: "EMAIL"}}}
	merged, err := mergeHierarchyPIIFilters(child, parent)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Mode != "fail_on_match" || !merged.SecretsEnabled || !normalizePIIApplyTo(merged.ApplyTo).system || !normalizePIIApplyTo(merged.ApplyTo).messages || len(merged.Rules) != 2 {
		t.Fatalf("parent/child PII policy was not narrowed: %#v", merged)
	}
	quota := mergeHierarchyQuotas(QuotaConfig{Day: BudgetConfig{Tokens: 20}}, QuotaConfig{Day: BudgetConfig{Tokens: 10}, Month: BudgetConfig{Requests: 5}})
	if quota.Day.Tokens != 10 || quota.Month.Requests != 5 {
		t.Fatalf("budget inheritance widened a parent limit: %#v", quota)
	}
}

func TestHierarchySuspendedCallerMappingSurvivesReloadAndRejectsRequests(t *testing.T) {
	t.Setenv("MOCK_API_KEY", "hierarchy-reload-test-key")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "response", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	defer upstream.Close()
	dbPath := serveAuthE2EDB(t)
	runner, closeDB, err := ConfigControlPlaneMigrationRunner(UsageDBConfig{Driver: "sqlite", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeDB() })
	applyConfigControlPlaneMigrationsForTest(t, runner)
	canonical := strings.Replace(serveAuthCanonicalYAML(), "https://mock.example/v1", upstream.URL, 1)
	canonical = strings.Replace(canonical, "server:\n  identifiers:", `server:
  hierarchy:
    parent:
      children:
        - id: team-a
          caller_id: alice
          organization_id: org-a
          team_id: team-a
          allowed_model_groups: [default]
  identifiers:`, 1)
	bobHash, err := HashCallerToken(tokenHashTestToken, strings.NewReader(strings.Repeat("b", callerTokenHashSaltBytes)), callerTokenHashMinIterations)
	if err != nil {
		t.Fatal(err)
	}
	canonical = strings.Replace(canonical, "callers:\n", `callers:
  - id: bob
    owner_user: bob
    project: search
    environment: dev
    status: active
    token_hash: "`+bobHash+`"
    token_id: `+tokenHashTestTokenID+`
    allow: [default]
`, 1)
	setID, err := ImportCanonicalYAML(runner.db, "staging", "hierarchy-active", []byte(canonical), "test-actor")
	if err != nil {
		t.Fatalf("import active hierarchy config: %v", err)
	}
	if err := ActivateConfigSet(runner.db, "staging", setID, "test-actor"); err != nil {
		t.Fatal(err)
	}
	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(bootstrapPath, []byte(serveAuthBootstrapYAML(t, dbPath, "staging")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigForServe(bootstrapPath)
	if err != nil {
		t.Fatalf("load active config: %v", err)
	}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.StatePath = filepath.Join(t.TempDir(), "router-state.json")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	serve := func(path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rec, req)
		return rec
	}
	policyRequest := httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	policyRequest.Header.Set("Authorization", "Bearer "+serveAuthToken)
	policyResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(policyResponse, policyRequest)
	if policyResponse.Code != http.StatusOK {
		t.Fatalf("active mapped policy status=%d body=%s", policyResponse.Code, policyResponse.Body.String())
	}
	if response := serve("/v1/chat/completions", serveAuthToken, `{"model":"default","messages":[{"role":"user","content":"hello"}]}`); response.Code != http.StatusOK {
		t.Fatalf("active inference status=%d body=%s", response.Code, response.Body.String())
	}
	unmapped := httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	unmapped.Header.Set("Authorization", "Bearer "+tokenHashTestToken)
	unmappedResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(unmappedResponse, unmapped)
	if unmappedResponse.Code != http.StatusForbidden || !strings.Contains(unmappedResponse.Body.String(), "hierarchy_child_not_configured") {
		t.Fatalf("unmapped caller status=%d body=%s, want unmapped 403", unmappedResponse.Code, unmappedResponse.Body.String())
	}

	suspendedCanonical := strings.Replace(canonical, "id: alice\n    owner_user: alice\n    project: search\n    environment: dev\n    status: active", "id: alice\n    owner_user: alice\n    project: search\n    environment: dev\n    status: suspended", 1)
	suspendedSet, err := ImportCanonicalYAML(runner.db, "staging", "hierarchy-suspended", []byte(suspendedCanonical), "test-actor")
	if err != nil {
		t.Fatalf("import suspended config: %v", err)
	}
	if err := ActivateConfigSet(runner.db, "staging", suspendedSet, "test-actor"); err != nil {
		t.Fatal(err)
	}
	if err := svc.reloadActiveConfigFromDB(); err != nil {
		t.Fatalf("reload suspended config: %v", err)
	}
	policyRequest = httptest.NewRequest(http.MethodGet, hierarchyPolicyPath, nil)
	policyRequest.Header.Set("Authorization", "Bearer "+serveAuthToken)
	policyResponse = httptest.NewRecorder()
	svc.Handler().ServeHTTP(policyResponse, policyRequest)
	if policyResponse.Code != http.StatusForbidden || !strings.Contains(policyResponse.Body.String(), "key-suspended") {
		t.Fatalf("suspended mapped policy status=%d body=%s, want suspended-key refusal distinct from unmapped 403", policyResponse.Code, policyResponse.Body.String())
	}
	if response := serve("/v1/chat/completions", serveAuthToken, `{"model":"default","messages":[{"role":"user","content":"hello"}]}`); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "key-suspended") {
		t.Fatalf("suspended inference status=%d body=%s, want suspended-key refusal", response.Code, response.Body.String())
	}
}

func TestHierarchyQuotaScopeUsesReservedNamespace(t *testing.T) {
	callerID := "hierarchy-org:org-a"
	scopes := hierarchyQuotaScopes(HierarchyConfig{Parent: HierarchyParentConfig{Children: []HierarchyChildPolicyConfig{{
		CallerID: "alice", OrganizationID: "org-a",
	}}}})
	if scopes["alice"] != hierarchyQuotaScopePrefix+"org-a" || safeDetectorID(hierarchyQuotaScopePrefix+"org-a") {
		t.Fatalf("organization quota scope is not collision-proof: %q", scopes["alice"])
	}
	if scopes["alice"] == callerID {
		t.Fatal("organization quota scope collided with a valid caller ID")
	}
}

func TestHierarchyModelGroupNamesAllowRouterDots(t *testing.T) {
	caller := CallerConfig{ID: "alice", Status: "active", Allow: []string{"gpt-5.1"}}
	hierarchy := HierarchyConfig{Parent: HierarchyParentConfig{Children: []HierarchyChildPolicyConfig{{
		ID: "team-a", CallerID: "alice", OrganizationID: "org-a", TeamID: "team-a", AllowedGroups: []string{"gpt-5.1"},
	}}}}
	if err := validateHierarchyConfig(hierarchy); err != nil {
		t.Fatalf("valid model group rejected: %v", err)
	}
	if err := validateHierarchyCallers(hierarchy, []CallerConfig{caller}); err != nil {
		t.Fatalf("valid model group rejected for caller: %v", err)
	}
	if validHierarchyModelGroupName("gpt 5.1") {
		t.Fatal("model group with whitespace was accepted")
	}
}

func TestHierarchyOrganizationBudgetIsSharedAcrossChildCallers(t *testing.T) {
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	cfg.Callers[0].Quota.Month.Tokens = 100
	addTokenHashTestCaller(t, cfg)
	cfg.Server.Hierarchy.Parent.Children = []HierarchyChildPolicyConfig{
		{ID: "team-a", CallerID: "alice", OrganizationID: "org-a", TeamID: "team-a", AllowedGroups: []string{"default"}},
		{ID: "team-b", CallerID: "bob", OrganizationID: "org-a", TeamID: "team-b", AllowedGroups: []string{"default"}},
	}
	invalid := *cfg
	invalid.Callers = append([]CallerConfig(nil), cfg.Callers...)
	invalid.Callers[1].Quota.Month.Tokens++
	if err := invalid.Validate(); err == nil {
		t.Fatal("organization accepted inconsistent parent quotas")
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := svc.quota.Admit(svc.quota.callers["alice"], 60)
	if !first.OK {
		t.Fatalf("first org request admission: %#v", first)
	}
	concurrent := svc.quota.Admit(svc.quota.callers["bob"], 50)
	if concurrent.OK || concurrent.Reason != "quota-exhausted" {
		t.Fatalf("concurrent child reservation escaped shared org budget: %#v", concurrent)
	}
	if _, _, err := svc.quota.RecordTokens(svc.quota.callers["alice"], first.Reservation, Usage{InputTokens: 60, TotalTokens: 60}); err != nil {
		t.Fatal(err)
	}
	svc.Close()
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	second := restarted.quota.Admit(restarted.quota.callers["bob"], 60)
	if second.OK || second.Reason != "quota-exhausted" {
		t.Fatalf("second child caller did not share the parent org budget: %#v", second)
	}
}

func TestHierarchyThreeRoutersInheritPolicyAndEnforceParentBudget(t *testing.T) {
	t.Setenv("HIERARCHY_ROOT_TOKEN", testToken)
	t.Setenv("HIERARCHY_TEAM_TOKEN", testToken)
	var upstreamCalls atomic.Int32
	var upstreamBody atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		upstreamBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-hierarchy","object":"chat.completion","created":1,"model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":90,"total_tokens":95}}`))
	}))
	defer provider.Close()

	rootCfg := testConfig(t, provider.URL, "provider-key", t.TempDir())
	rootCfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "root-usage.sqlite"))
	rootGroup := rootCfg.Models["default"]
	rootGroup.Targets[0].ToolSupport = anthropicToolTarget().ToolSupport
	rootCfg.Models["default"] = rootGroup
	rootCfg.Callers[0].Quota.Month.Tokens = 100
	rootCfg.Callers[0].TrustTraceparent = true
	rootCfg.Server.Hierarchy.Parent.Children = []HierarchyChildPolicyConfig{{
		ID: "team", CallerID: "alice", OrganizationID: "org-a", TeamID: "platform",
		AllowedGroups: []string{"default"}, ToolAllowList: []string{"read"}, PIIFilter: testPIIFilterConfig("redact_only"),
	}}
	root, err := New(rootCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	rootServer := httptest.NewServer(root.Handler())
	defer rootServer.Close()

	teamCfg := testConfig(t, rootServer.URL, "", t.TempDir())
	teamCfg.Callers[0].TrustTraceparent = true
	teamCfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "team-usage.sqlite"))
	teamProvider := teamCfg.Provider["mock"]
	teamProvider.APIKey = testToken
	teamProvider.UpstreamKind = "router"
	teamCfg.Provider["mock"] = teamProvider
	teamGroup := teamCfg.Models["default"]
	teamGroup.Targets[0].Model = "default"
	teamGroup.Targets[0].ToolSupport = anthropicToolTarget().ToolSupport
	teamCfg.Models["default"] = teamGroup
	teamCfg.Server.Hierarchy.Child = HierarchyChildConfig{ID: "team", ParentURL: rootServer.URL, CredentialEnv: "HIERARCHY_ROOT_TOKEN", CacheTTL: "30s"}
	teamCfg.Server.Hierarchy.Parent.Children = []HierarchyChildPolicyConfig{{
		ID: "app", CallerID: "alice", OrganizationID: "org-a", TeamID: "team-a",
		AllowedGroups: []string{"default"}, ToolAllowList: []string{"read"},
	}}
	team, err := New(teamCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	teamServer := httptest.NewServer(team.Handler())
	defer teamServer.Close()

	appCfg := testConfig(t, teamServer.URL, "", t.TempDir())
	appCfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "app-usage.sqlite"))
	appProvider := appCfg.Provider["mock"]
	appProvider.APIKey = testToken
	appProvider.UpstreamKind = "router"
	appCfg.Provider["mock"] = appProvider
	appGroup := appCfg.Models["default"]
	appGroup.Targets[0].Model = "default"
	appGroup.Targets[0].ToolSupport = anthropicToolTarget().ToolSupport
	appCfg.Models["default"] = appGroup
	appCfg.Server.Hierarchy.Child = HierarchyChildConfig{ID: "app", ParentURL: teamServer.URL, CredentialEnv: "HIERARCHY_TEAM_TOKEN", CacheTTL: "30s"}
	app, err := New(appCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	requestBody := `{"model":"default","messages":[{"role":"user","content":"contact alice@example.com"}],"max_tokens":16,"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("three-level request status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(upstreamBody.Load().(string), "alice@example.com") {
		t.Fatal("inherited parent PII rule did not redact the request before provider dispatch")
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("provider calls=%d, want 1", upstreamCalls.Load())
	}
	rootTrace := assertHierarchyAuditUsage(t, root, "org-a", "platform")
	teamTrace := assertHierarchyAuditUsage(t, team, "org-a", "team-a")
	appTrace := assertHierarchyAuditUsage(t, app, "org-a", "team-a")
	if rootTrace != teamTrace || teamTrace != appTrace {
		t.Fatalf("three-router trace IDs differ: root=%q team=%q app=%q", rootTrace, teamTrace, appTrace)
	}

	blockedTool := strings.Replace(requestBody, `"name":"read"`, `"name":"write"`, 1)
	request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(blockedTool))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || upstreamCalls.Load() != 1 {
		t.Fatalf("disallowed tool status=%d provider_calls=%d body=%s", response.Code, upstreamCalls.Load(), response.Body.String())
	}

	overspend := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	overspend.Header.Set("Authorization", "Bearer "+testToken)
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, overspend)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "upstream-quota-exhausted") || upstreamCalls.Load() != 1 {
		t.Fatalf("parent budget status=%d provider_calls=%d body=%s", response.Code, upstreamCalls.Load(), response.Body.String())
	}
}

func assertHierarchyAuditUsage(t *testing.T, svc *Service, organizationID, teamID string) string {
	t.Helper()
	if svc == nil || svc.usage == nil {
		t.Fatal("hierarchy audit service has no usage store")
	}
	if svc.usage.writer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := svc.usage.writer.flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	rows, err := svc.usage.rows(UsageReportOptions{From: now.Add(-time.Minute), To: now.Add(time.Minute), OrganizationID: organizationID, TeamID: teamID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("hierarchy filtered usage rows=%+v", rows)
	}
	row := rows[0]
	if row.OrganizationID != organizationID || row.TeamID != teamID || row.TraceID == "" || row.SpanID == "" {
		t.Fatalf("hierarchy usage metadata=%+v", row)
	}
	if exported := adminRequestFromRow(row); exported.HopID != row.SpanID || exported.TraceID != row.TraceID || exported.OrganizationID != organizationID || exported.TeamID != teamID {
		t.Fatalf("hierarchy export metadata=%+v", exported)
	}
	rowJSON, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var events []requestTraceEventRecord
	if err := svc.usage.db.Where("request_id = ?", row.RequestID).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	eventJSON, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	logJSON, err := os.ReadFile(svc.cfg.Server.Logging.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{testToken, "contact alice@example.com", "alice@example.com"} {
		if strings.Contains(string(rowJSON), forbidden) || strings.Contains(string(eventJSON), forbidden) || strings.Contains(string(logJSON), forbidden) {
			t.Fatalf("hierarchy persisted secret or prompt value %q", forbidden)
		}
	}
	return row.TraceID
}
