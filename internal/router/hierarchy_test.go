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

	"gopkg.in/yaml.v3"
)

func TestHierarchyParentPublishesCallerBoundPolicy(t *testing.T) {
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
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
	if _, err := runtime.refresh(context.Background(), now.Add(31*time.Second)); err == nil {
		t.Fatal("unavailable parent did not fail closed after TTL")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("parent calls=%d, want initial fetch and one post-expiry attempt", got)
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
	parent := PIIFilterConfig{Enabled: true, Mode: "redact_only", ApplyTo: PIIFilterApplyTo{Messages: boolPtr(true)}, Rules: []PIIFilterRule{{Name: "parent_email", Expression: `@example\\.test`, PlaceholderPrefix: "PARENT_EMAIL"}}}
	child := PIIFilterConfig{Enabled: true, Mode: "fail_on_match", ApplyTo: PIIFilterApplyTo{System: boolPtr(true)}, Rules: []PIIFilterRule{{Name: "email", Expression: `@example\\.test`, PlaceholderPrefix: "EMAIL"}}}
	merged, err := mergeHierarchyPIIFilters(child, parent)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Mode != "fail_on_match" || !normalizePIIApplyTo(merged.ApplyTo).system || !normalizePIIApplyTo(merged.ApplyTo).messages || len(merged.Rules) != 2 {
		t.Fatalf("parent/child PII policy was not narrowed: %#v", merged)
	}
	quota := mergeHierarchyQuotas(QuotaConfig{Day: BudgetConfig{Tokens: 20}}, QuotaConfig{Day: BudgetConfig{Tokens: 10}, Month: BudgetConfig{Requests: 5}})
	if quota.Day.Tokens != 10 || quota.Month.Requests != 5 {
		t.Fatalf("budget inheritance widened a parent limit: %#v", quota)
	}
}

func TestHierarchyOrganizationBudgetIsSharedAcrossChildCallers(t *testing.T) {
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	cfg.Callers[0].Quota.Month.Tokens = 100
	bobToken := "hierarchy-bob-token"
	bobHash := sha256.Sum256([]byte(bobToken))
	bob := cfg.Callers[0]
	bob.ID, bob.TokenID, bob.TokenSHA256 = "bob", "rtr_bob_test", hex.EncodeToString(bobHash[:])
	cfg.Callers = append(cfg.Callers, bob)
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
	assertHierarchyAuditUsage(t, root, "org-a", "platform")
	assertHierarchyAuditUsage(t, team, "org-a", "team-a")
	assertHierarchyAuditUsage(t, app, "org-a", "team-a")

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

func assertHierarchyAuditUsage(t *testing.T, svc *Service, organizationID, teamID string) {
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
}
