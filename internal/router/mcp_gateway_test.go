// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPGatewayConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  MCPGatewayConfig
		want bool
	}{
		{name: "disabled empty"},
		{name: "disabled with settings", cfg: MCPGatewayConfig{Servers: []MCPServerConfig{{Name: "local", URL: "http://localhost"}}}, want: true},
		{name: "enabled without servers", cfg: MCPGatewayConfig{Enabled: true}, want: true},
		{name: "valid", cfg: MCPGatewayConfig{Enabled: true, RPM: 60, Servers: []MCPServerConfig{{Name: "local", URL: "http://localhost", AllowHosts: []string{"localhost"}}}, Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"local/echo"}, DenyByDefault: true}}}},
		{name: "cleartext remote rejected even when allowed", cfg: MCPGatewayConfig{Enabled: true, RPM: 60, Servers: []MCPServerConfig{{Name: "remote", URL: "http://mcp.example.test", AllowHosts: []string{"mcp.example.test"}, AllowHTTP: true}}}, want: true},
		{name: "invalid url", cfg: MCPGatewayConfig{Enabled: true, Servers: []MCPServerConfig{{Name: "local", URL: "file:///tmp/mcp"}}}, want: true},
		{name: "credential in url", cfg: MCPGatewayConfig{Enabled: true, Servers: []MCPServerConfig{{Name: "local", URL: "https://example.test?token=not-allowed"}}}, want: true},
		{name: "invalid credential env", cfg: MCPGatewayConfig{Enabled: true, RPM: 60, Servers: []MCPServerConfig{{Name: "local", URL: "https://example.test", AllowHosts: []string{"example.test"}, CredentialEnv: "not a var"}}}, want: true},
		{name: "provider credential env rejected", cfg: MCPGatewayConfig{Enabled: true, RPM: 60, Servers: []MCPServerConfig{{Name: "local", URL: "https://example.test", AllowHosts: []string{"example.test"}, CredentialEnv: "OPENAI_API_KEY"}}}, want: true},
		{name: "unknown server policy", cfg: MCPGatewayConfig{Enabled: true, RPM: 60, Servers: []MCPServerConfig{{Name: "local", URL: "https://example.test", AllowHosts: []string{"example.test"}}}, Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"other/echo"}}}}, want: true},
		{name: "missing policy identity", cfg: MCPGatewayConfig{Enabled: true, RPM: 60, Servers: []MCPServerConfig{{Name: "local", URL: "https://example.test", AllowHosts: []string{"example.test"}}}, Policies: []MCPPolicyConfig{{Tools: []string{"local/echo"}}}}, want: true},
		{name: "non-allowlisted host", cfg: MCPGatewayConfig{Enabled: true, RPM: 60, Servers: []MCPServerConfig{{Name: "local", URL: "https://other.example.test", AllowHosts: []string{"example.test"}}}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateMCPGateway(tt.cfg, false); (err != nil) != tt.want {
				t.Fatalf("validateMCPGateway() error=%v, want error %v", err, tt.want)
			}
		})
	}
}

func TestMCPGatewayRequiresConfiguredStaticCredentialAtServe(t *testing.T) {
	const env = "ROUTER_MCP_GATEWAY_REQUIRED_CREDENTIAL_TEST"
	t.Setenv(env, "")
	cfg := MCPGatewayConfig{Enabled: true, RPM: 60, Servers: []MCPServerConfig{{Name: "local", URL: "https://example.test", AllowHosts: []string{"example.test"}, CredentialEnv: env}}}
	if err := validateMCPGateway(cfg, true); err == nil {
		t.Fatal("unset static credential accepted for serving")
	}
	if err := validateMCPGateway(cfg, false); err != nil {
		t.Fatalf("unset static credential rejected for import: %v", err)
	}
	t.Setenv(env, "test-only-upstream-key")
	if err := validateMCPGateway(cfg, true); err != nil {
		t.Fatalf("configured static credential rejected for serving: %v", err)
	}
}

func TestMCPGatewayEgressBlocksMetadataAndUnlistedHost(t *testing.T) {
	metadata := net.ParseIP("169.254.169.254")
	lookedUp, dialed := false, false
	client := newEgressHTTPClientWithOptions(egressHTTPClientOptions{
		Timeout: time.Second, AllowHosts: []string{"mcp.example.test"}, Label: "test MCP",
		Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			lookedUp = true
			return []net.IP{metadata}, nil
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, fmt.Errorf("unexpected dial")
		},
	})
	request, _ := http.NewRequest(http.MethodPost, "https://mcp.example.test/rpc", nil)
	if _, err := client.Do(request); err == nil {
		t.Fatal("metadata destination was accepted")
	}
	client.CloseIdleConnections()
	if !lookedUp || dialed || egressIPAllowedForHost("mcp.example.test", metadata) {
		t.Fatalf("metadata check lookup=%v dial=%v allowed=%v", lookedUp, dialed, egressIPAllowedForHost("mcp.example.test", metadata))
	}
	lookedUp = false
	request, _ = http.NewRequest(http.MethodPost, "https://other.example.test/rpc", nil)
	if _, err := client.Do(request); err == nil {
		t.Fatal("non-allowlisted host was accepted")
	}
	if lookedUp {
		t.Fatal("non-allowlisted host reached DNS lookup")
	}
}

func TestMCPGatewayPolicyReferences(t *testing.T) {
	cfg := MCPGatewayConfig{Enabled: true, Policies: []MCPPolicyConfig{{Caller: "alice", Project: "project_A", ModelGroup: "coder"}}}
	callers := []CallerConfig{{ID: "alice", Project: "project-a"}}
	projects := []ProjectConfig{{ID: "project-a"}}
	models := map[string]ModelGroup{"coder": {Strategy: "static"}}
	if err := validateMCPPolicyReferences(cfg, callers, projects, models); err != nil {
		t.Fatalf("valid policy references rejected: %v", err)
	}
	cases := []struct {
		name string
		edit func(*MCPPolicyConfig)
	}{
		{name: "caller", edit: func(policy *MCPPolicyConfig) { policy.Caller = "unknown" }},
		{name: "project", edit: func(policy *MCPPolicyConfig) { policy.Project = "unknown" }},
		{name: "model group", edit: func(policy *MCPPolicyConfig) { policy.ModelGroup = "unknown" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := cfg.Policies[0]
			tc.edit(&policy)
			bad := cfg
			bad.Policies = []MCPPolicyConfig{policy}
			if err := validateMCPPolicyReferences(bad, callers, projects, models); err == nil {
				t.Fatal("unknown policy reference accepted")
			}
		})
	}
}

func TestMCPModelGroupResolutionUsesServerDefaultOrOnlyCallerGroup(t *testing.T) {
	caller := &callerRuntime{allow: map[string]bool{"default": true, "other": true}}
	cfg := &Config{Models: map[string]ModelGroup{"default": {PIIFilter: PIIFilterConfig{Enabled: true}}, "other": {}}}
	if _, err := resolveMCPModelGroup(cfg, caller, ""); err == nil || !strings.Contains(err.Error(), "model group is required") {
		t.Fatalf("unresolved group with governance did not fail closed: %v", err)
	}
	cfg.Server.DefaultModelGroup = "default"
	if got, err := resolveMCPModelGroup(cfg, caller, ""); err != nil || got != "default" {
		t.Fatalf("server default resolution got=%q err=%v", got, err)
	}
	if got, err := resolveMCPModelGroup(cfg, caller, "other"); err != nil || got != "other" {
		t.Fatalf("header resolution got=%q err=%v", got, err)
	}
	cfg.Server.DefaultModelGroup = ""
	cfg.Models["default"] = ModelGroup{PIIFilter: PIIFilterConfig{Enabled: true}}
	caller.allow = map[string]bool{"default": true}
	if got, err := resolveMCPModelGroup(cfg, caller, ""); err != nil || got != "default" {
		t.Fatalf("single allowed group fallback got=%q err=%v", got, err)
	}
}

func TestMCPProjectPolicyUsesAccountSlugNormalization(t *testing.T) {
	caller := &callerRuntime{cfg: CallerConfig{ID: "alice"}, project: "mixed-project"}
	cfg := &Config{MCP: MCPGatewayConfig{Enabled: true, Policies: []MCPPolicyConfig{{Project: "Mixed_Project", Tools: []string{"server/read"}, DenyByDefault: true}}}}
	svc := &Service{cfg: cfg}
	if !svc.mcpToolAllowed(caller, "", "server/read") || svc.mcpToolAllowed(caller, "", "server/write") {
		t.Fatal("mixed-case/underscore project policy did not match its normalized caller project")
	}
	if err := validateMCPPolicyReferences(cfg.MCP, []CallerConfig{{ID: "alice", Project: "Mixed_Project"}}, nil, nil); err != nil {
		t.Fatalf("normalized caller project reference rejected: %v", err)
	}
}

func TestMCPToolLimitMatchingNormalizesConfigWhitespace(t *testing.T) {
	limits := matchingMCPToolLimits([]MCPToolLimitConfig{{Caller: " alice ", Tool: " demo/read ", RPM: 1}}, "alice", "demo/read")
	if len(limits) != 1 || limits[0].Tool != "demo/read" || limits[0].Caller != "alice" {
		t.Fatalf("tool limit did not normalize to runtime identity: %+v", limits)
	}
}

func TestMCPGatewayToolPolicyDenyByDefault(t *testing.T) {
	caller := &callerRuntime{cfg: CallerConfig{ID: "alice"}, project: "project-a"}
	svc := &Service{cfg: &Config{MCP: MCPGatewayConfig{Enabled: true, Policies: []MCPPolicyConfig{
		{Project: "project-a", Tools: []string{"server/read"}, DenyByDefault: true},
	}}}}
	if !svc.mcpToolAllowed(caller, "", "server/read") {
		t.Fatal("listed tool was denied")
	}
	if svc.mcpToolAllowed(caller, "", "server/write") {
		t.Fatal("unlisted tool was allowed")
	}
	if svc.mcpToolAllowed(nil, "", "server/read") {
		t.Fatal("missing caller was allowed")
	}
	svc.cfg.MCP.Policies = append(svc.cfg.MCP.Policies, MCPPolicyConfig{Caller: "alice", Tools: []string{"server/write"}, DenyByDefault: true})
	if svc.mcpToolAllowed(caller, "", "server/write") {
		t.Fatal("caller policy overrode the project policy")
	}
}

func TestMCPGatewayAuthenticatedToolsListAndCall(t *testing.T) {
	var upstreamAuth string
	var failToolCall bool
	var failToolResult bool
	var sessionCloseCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			sessionCloseCalls++
			w.WriteHeader(http.StatusAccepted)
			return
		}
		upstreamAuth = r.Header.Get("Authorization")
		var request struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode upstream request: %v", err)
			return
		}
		if request.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "mcp-session-test" {
			t.Errorf("upstream session header=%q", r.Header.Get("Mcp-Session-Id"))
		}
		if request.Method == "initialize" && r.Header.Get("MCP-Protocol-Version") != "" {
			t.Errorf("initialize unexpectedly sent protocol header %q", r.Header.Get("MCP-Protocol-Version"))
		}
		if request.Method != "initialize" && r.Header.Get("MCP-Protocol-Version") != "2025-11-25" {
			t.Errorf("upstream protocol header=%q", r.Header.Get("MCP-Protocol-Version"))
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "mcp-session-test")
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{
				map[string]any{"name": "read", "inputSchema": map[string]any{"type": "object"}},
				map[string]any{"name": "write", "inputSchema": map[string]any{"type": "object"}},
			}}})
		case "tools/call":
			if failToolCall {
				http.Error(w, "upstream secret detail", http.StatusBadGateway)
				return
			}
			if failToolResult {
				writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "tool failed"}}}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": request.Params.Arguments["text"]}}}})
		default:
			t.Errorf("unexpected upstream method %q", request.Method)
		}
	}))
	defer upstream.Close()
	t.Setenv("ROUTER_MCP_GATEWAY_TEST_CREDENTIAL", "server-side-value")
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.db"))
	cfg.MCP = MCPGatewayConfig{
		Enabled:  true,
		Servers:  []MCPServerConfig{{Name: "demo", URL: upstream.URL, CredentialEnv: "ROUTER_MCP_GATEWAY_TEST_CREDENTIAL", AllowHosts: []string{"127.0.0.1"}}},
		Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read"}, DenyByDefault: true}},
	}
	svc, err := newService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	listed := call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "demo/read") || strings.Contains(listed.Body.String(), "demo/write") {
		t.Fatalf("tools/list status=%d body=%s", listed.Code, listed.Body.String())
	}
	called := call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"demo/read","arguments":{"text":"hello"}}}`)
	if called.Code != http.StatusOK || !strings.Contains(called.Body.String(), "hello") {
		t.Fatalf("tools/call status=%d body=%s", called.Code, called.Body.String())
	}
	if upstreamAuth != "Bearer server-side-value" {
		t.Fatalf("upstream credential was not set server-side")
	}
	var audit mcpToolAuditEventRecord
	if err := svc.usage.db.Where("tool = ?", "demo/read").First(&audit).Error; err != nil {
		t.Fatalf("MCP invocation audit missing: %v", err)
	}
	if audit.CallerID != "alice" || audit.ResultStatus != "success" || audit.LatencyMS < 0 || audit.ArgumentsSHA256 == "" || strings.Contains(audit.ArgumentsSHA256, "hello") {
		t.Fatalf("unsafe or incomplete MCP audit row: %+v", audit)
	}
	failToolCall = true
	failed := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"demo/read","arguments":{"text":"secret-tool-argument"}}}`)
	if failed.Code != http.StatusOK || strings.Contains(failed.Body.String(), "upstream secret detail") {
		t.Fatalf("upstream error was not safely hidden: %s", failed.Body.String())
	}
	failedDigest, _ := mcpToolArgumentsDigest(svc.mcpAuditKey[:], map[string]any{"text": "secret-tool-argument"})
	var auditRows []mcpToolAuditEventRecord
	if err := svc.usage.db.Where("tool = ?", "demo/read").Find(&auditRows).Error; err != nil {
		t.Fatalf("read MCP invocation audit: %v", err)
	}
	foundFailedAudit := false
	for _, row := range auditRows {
		if row.ArgumentsSHA256 == failedDigest {
			audit = row
			foundFailedAudit = true
			break
		}
	}
	if !foundFailedAudit {
		t.Fatalf("failed MCP invocation audit missing: response=%s rows=%+v", failed.Body.String(), auditRows)
	}
	if audit.ResultStatus != "error" || strings.Contains(audit.ArgumentsSHA256, "secret-tool-argument") {
		t.Fatalf("failed invocation audit is incomplete or unsafe: %+v", audit)
	}
	failToolCall = false
	failToolResult = true
	toolError := call(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"demo/read","arguments":{"text":"structured error"}}}`)
	if toolError.Code != http.StatusOK || !strings.Contains(toolError.Body.String(), `"isError":true`) {
		t.Fatalf("tool error result was not forwarded: status=%d body=%s", toolError.Code, toolError.Body.String())
	}
	toolErrorDigest, _ := mcpToolArgumentsDigest(svc.mcpAuditKey[:], map[string]any{"text": "structured error"})
	var toolErrorAudit *mcpToolAuditEventRecord
	var allToolAudits []mcpToolAuditEventRecord
	if err := svc.usage.db.Where("tool = ?", "demo/read").Find(&allToolAudits).Error; err != nil {
		t.Fatal(err)
	}
	for i := range allToolAudits {
		if allToolAudits[i].ArgumentsSHA256 == toolErrorDigest {
			toolErrorAudit = &allToolAudits[i]
			break
		}
	}
	if toolErrorAudit == nil || toolErrorAudit.ResultStatus != "error" {
		t.Fatalf("isError result not audited as error: digest=%s audits=%+v", toolErrorDigest, allToolAudits)
	}
	failToolResult = false
	currentRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"demo/read","arguments":{"text":"hello current protocol"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`))
	currentRequest.Header.Set("Authorization", "Bearer "+testToken)
	currentRequest.Header.Set("MCP-Protocol-Version", "2026-07-28")
	currentRequest.Header.Set("Mcp-Method", "tools/call")
	currentRequest.Header.Set("Mcp-Name", "demo/read")
	currentResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(currentResponse, currentRequest)
	if currentResponse.Code != http.StatusOK || currentResponse.Header().Get("MCP-Protocol-Version") != "2026-07-28" || !strings.Contains(currentResponse.Body.String(), "hello current protocol") {
		t.Fatalf("current protocol response status=%d headers=%v body=%s", currentResponse.Code, currentResponse.Header(), currentResponse.Body.String())
	}
	headerMismatch := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":21,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`))
	headerMismatch.Header.Set("Authorization", "Bearer "+testToken)
	headerMismatch.Header.Set("MCP-Protocol-Version", "2026-07-28")
	headerMismatch.Header.Set("Mcp-Method", "tools/call")
	mismatchResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(mismatchResponse, headerMismatch)
	if mismatchResponse.Code != http.StatusBadRequest || !strings.Contains(mismatchResponse.Body.String(), "-32020") {
		t.Fatalf("header mismatch status=%d body=%s", mismatchResponse.Code, mismatchResponse.Body.String())
	}
	badOrigin := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":22,"method":"tools/list"}`))
	badOrigin.Header.Set("Authorization", "Bearer "+testToken)
	badOrigin.Header.Set("Origin", "https://attacker.example")
	originResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(originResponse, badOrigin)
	if originResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", originResponse.Code, originResponse.Body.String())
	}
	denied := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"demo/write","arguments":{}}}`)
	if denied.Code != http.StatusOK || !strings.Contains(denied.Body.String(), "tool denied by policy") {
		t.Fatalf("denied call status=%d body=%s", denied.Code, denied.Body.String())
	}
	var deniedAudit mcpToolAuditEventRecord
	if err := svc.usage.db.Where("tool = ?", "demo/write").First(&deniedAudit).Error; err != nil {
		t.Fatalf("denied invocation was not audited: %v", err)
	}
	if deniedAudit.ResultStatus != "denied" {
		t.Fatalf("denied invocation result status=%q", deniedAudit.ResultStatus)
	}
	if sessionCloseCalls == 0 {
		t.Fatal("upstream sessions were not closed")
	}
	initialize := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"initialize","params":{"protocolVersion":"unsupported-version"}}`))
	initialize.Header.Set("Authorization", "Bearer "+testToken)
	initializeResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(initializeResponse, initialize)
	if initializeResponse.Code != http.StatusOK || initializeResponse.Header().Get("MCP-Protocol-Version") != mcpVersionCurrent || !strings.Contains(initializeResponse.Body.String(), `"protocolVersion":"`+mcpVersionCurrent+`"`) {
		t.Fatalf("unsupported requested protocol was not negotiated: status=%d headers=%v body=%s", initializeResponse.Code, initializeResponse.Header(), initializeResponse.Body.String())
	}
	unauthenticated := httptest.NewRecorder()
	svc.Handler().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.Code)
	}
}

func TestMCPGatewayMasksToolArgumentsResultsAndExportsAudit(t *testing.T) {
	const apiKey = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	var upstreamText string
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode upstream MCP request: %v", err)
			return
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "mask-session")
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": mcpVersionLegacy}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			upstreamCalls++
			upstreamText, _ = request.Params.Arguments["text"].(string)
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "result: " + apiKey + " user@example.test"}}}})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{}}})
		}
	}))
	defer upstream.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "mcp-mask.sqlite"))
	inheritedFilter := PIIFilterConfig{
		Enabled: true, SecretsEnabled: true, Mode: "redact_only",
		Rules: []PIIFilterRule{{Name: "email", Expression: `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`, PlaceholderPrefix: "EMAIL"}},
	}
	cfg.Server.DefaultModelGroup = ""
	cfg.Callers[0].Allow = []string{"default"}
	cfg.MCP = MCPGatewayConfig{
		Enabled:  true,
		Servers:  []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}},
		Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read"}, DenyByDefault: true}},
	}
	svc, err := newService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.hierarchyPolicy.Store(&hierarchyPolicyRuntime{
		ttl: time.Minute, fetchedAt: time.Now(),
		policy: HierarchyPolicy{AllowedGroups: []string{"default"}, PIIFilter: inheritedFilter},
	})
	call := func(text string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"demo/read","arguments":{"text":%q}}}`, text)
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	w := call(apiKey + " user@example.test")
	if w.Code != http.StatusOK {
		t.Fatalf("MCP call status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(upstreamText, apiKey) || strings.Contains(upstreamText, "user@example.test") || !strings.Contains(upstreamText, "[SECRET_PROVIDER_1]") || !strings.Contains(upstreamText, "[EMAIL_1]") {
		t.Fatalf("upstream arguments were not masked: %q", upstreamText)
	}
	if strings.Contains(w.Body.String(), apiKey) || strings.Contains(w.Body.String(), "user@example.test") || !strings.Contains(w.Body.String(), "[SECRET_PROVIDER_1]") || !strings.Contains(w.Body.String(), "[EMAIL_1]") {
		t.Fatalf("tool result was not masked: %s", w.Body.String())
	}
	var exported adminAuditExportDocument
	exportRequest := httptest.NewRequest(http.MethodGet, "/admin/v1/audit-events/export", nil)
	exportRequest = exportRequest.WithContext(WithAdminAuditMeta(exportRequest.Context(), AdminAuditMeta{Subject: "auditor", Domain: string(AdminDomainCredential)}))
	exportResponse := httptest.NewRecorder()
	adminAuditExportHandlers(svc.usage.db, nil)["admin.audit.export"](exportResponse, exportRequest)
	if exportResponse.Code != http.StatusOK {
		t.Fatalf("audit export status=%d body=%s", exportResponse.Code, exportResponse.Body.String())
	}
	if err := json.Unmarshal(exportResponse.Body.Bytes(), &exported); err != nil {
		t.Fatalf("decode audit export: %v", err)
	}
	if err := VerifyAdminAuditExport(strings.NewReader(exportResponse.Body.String())); err != nil {
		t.Fatalf("verify MCP audit export: %v", err)
	}
	if len(exported.Items) != 1 || exported.Items[0].PrincipalSubject != "alice" || exported.Items[0].Object != "mcp:tool" || exported.Items[0].Detail != "demo/read" || exported.Items[0].Outcome != "success" || exported.Items[0].LatencyMS == nil || *exported.Items[0].LatencyMS < 0 {
		t.Fatalf("export omitted safe MCP invocation metadata: %+v", exported.Items)
	}
	if strings.Contains(exportResponse.Body.String(), apiKey) || strings.Contains(exportResponse.Body.String(), "user@example.test") {
		t.Fatal("audit export contains an unmasked tool payload")
	}
	inheritedFilter.Mode = "fail_on_match"
	inheritedFilter.FailOnMatch = true
	runtimePolicy := svc.hierarchyPolicy.Load()
	runtimePolicy.policy.PIIFilter = inheritedFilter
	blockedInput := call(apiKey + " user@example.test")
	if blockedInput.Code != http.StatusOK || !strings.Contains(blockedInput.Body.String(), "tool call blocked by router policy") || strings.Contains(blockedInput.Body.String(), apiKey) || upstreamCalls != 1 {
		t.Fatalf("fail_on_match did not block MCP input before forwarding: status=%d calls=%d body=%s", blockedInput.Code, upstreamCalls, blockedInput.Body.String())
	}
	blockedResult := call("benign input")
	if blockedResult.Code != http.StatusOK || !strings.Contains(blockedResult.Body.String(), "tool result blocked by router policy") || strings.Contains(blockedResult.Body.String(), apiKey) || upstreamCalls != 2 {
		t.Fatalf("fail_on_match did not withhold MCP result: status=%d calls=%d body=%s", blockedResult.Code, upstreamCalls, blockedResult.Body.String())
	}
}

func TestMCPGatewayToolFirewallApprovalReleaseAndTimeout(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "approval-upstream-session")
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": mcpVersionLegacy}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			upstreamCalls++
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{}}})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{}}})
		}
	}))
	defer upstream.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "mcp-approval.sqlite"))
	cfg.Admin.Enabled = true
	cfg.Server.ToolFirewall = ToolFirewallConfig{
		Mode: toolFirewallEnforce, ApprovalTimeoutMS: 1000,
		Sequences: []ToolFirewallSequenceRule{{FirstTool: "demo/read", FirstArgsRegex: "account", ThenTool: "demo/write", WithinTurns: 3, Action: "hold"}},
	}
	cfg.MCP = MCPGatewayConfig{
		Enabled:  true,
		Servers:  []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}},
		Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read", "demo/write"}, DenyByDefault: true}},
		Limits:   []MCPToolLimitConfig{{Caller: "alice", Tool: "demo/write", RPM: 10, CostPerCallUSD: 0.01, HourlyUSD: 1, DailyUSD: 2}},
	}
	svc, err := newService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	adminHarness := newAdminV1Harness(t)
	adminHarness.mountWithExtra(t, map[string]http.HandlerFunc{"tool.approvals.resolve": svc.handleToolApprovalResolution})
	approvalNow := time.Now().UTC()
	svc.toolApprovals.now = func() time.Time { return approvalNow }
	call := func(name, text string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{"text":%q}}}`, name, text)
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("MCP-Session-Id", "sequence-session")
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	if got := call("demo/read", "account 17"); got.Code != http.StatusOK {
		t.Fatalf("sequence first call status=%d body=%s", got.Code, got.Body.String())
	}
	pending := call("demo/write", "send account 17")
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), "approval_pending") {
		t.Fatalf("risky MCP call was not held: status=%d body=%s", pending.Code, pending.Body.String())
	}
	assertMCPWriteCharges := func(wantRequests int, wantSpend float64) {
		t.Helper()
		svc.quota.mu.Lock()
		defer svc.quota.mu.Unlock()
		state := svc.quota.state.MCPToolLimits["alice\x00demo/write"]
		if wantRequests == 0 && state == nil {
			return
		}
		if state == nil || len(state.RequestTimes) != wantRequests || state.HourlyUSD != wantSpend {
			t.Fatalf("MCP write quota state=%+v, want requests=%d spend=%v", state, wantRequests, wantSpend)
		}
	}
	assertMCPWriteCharges(0, 0)
	var pendingEnvelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(pending.Body.Bytes(), &pendingEnvelope); err != nil || len(pendingEnvelope.Result.Content) != 1 {
		t.Fatalf("decode approval result: %v body=%s", err, pending.Body.String())
	}
	var approval struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal([]byte(pendingEnvelope.Result.Content[0].Text), &approval); err != nil || approval.ApprovalID == "" {
		t.Fatalf("approval id missing: %v text=%s", err, pendingEnvelope.Result.Content[0].Text)
	}
	releaseResponse := adminHarness.do(t, http.MethodPost, "/admin/v1/tool-approvals/"+approval.ApprovalID, map[string]any{"decision": "release"}, nil)
	if releaseResponse.Code != http.StatusOK || !strings.Contains(releaseResponse.Body.String(), `"decision":"release"`) {
		t.Fatalf("approval route release status=%d body=%s", releaseResponse.Code, releaseResponse.Body.String())
	}
	if got := adminHarness.authorize.snapshot(); len(got) != 1 || got[0].Object != authzObjectToolApprovals || got[0].Action != authzActionResolve {
		t.Fatalf("approval route did not take its declared authorization decision: %+v", got)
	}
	if got := call("demo/write", "send account 17"); got.Code != http.StatusOK || strings.Contains(got.Body.String(), "approval_pending") {
		t.Fatalf("released MCP call did not continue: status=%d body=%s", got.Code, got.Body.String())
	}
	assertMCPWriteCharges(1, 0.01)
	if got := call("demo/read", "account 22"); got.Code != http.StatusOK {
		t.Fatalf("second sequence first call status=%d body=%s", got.Code, got.Body.String())
	}
	timeoutPending := call("demo/write", "send account 22")
	if !strings.Contains(timeoutPending.Body.String(), "approval_pending") {
		t.Fatalf("timeout fixture was not held: %s", timeoutPending.Body.String())
	}
	var deniedApproval struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(timeoutPending.Body.Bytes(), &deniedApproval); err != nil || len(deniedApproval.Result.Content) != 1 {
		t.Fatalf("decode second approval response: %v body=%s", err, timeoutPending.Body.String())
	}
	var deniedID struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal([]byte(deniedApproval.Result.Content[0].Text), &deniedID); err != nil || deniedID.ApprovalID == "" {
		t.Fatalf("second approval ID missing: %v", err)
	}
	denyResponse := adminHarness.do(t, http.MethodPost, "/admin/v1/tool-approvals/"+deniedID.ApprovalID, map[string]any{"decision": "deny"}, nil)
	if denyResponse.Code != http.StatusOK || !strings.Contains(denyResponse.Body.String(), `"decision":"deny"`) {
		t.Fatalf("approval route deny status=%d body=%s", denyResponse.Code, denyResponse.Body.String())
	}
	deniedCall := call("demo/write", "send account 22")
	if !strings.Contains(deniedCall.Body.String(), `"isError":true`) || strings.Contains(deniedCall.Body.String(), "approval_pending") {
		t.Fatalf("MCP call was not denied after admin approval decision: %s", deniedCall.Body.String())
	}
	assertMCPWriteCharges(1, 0.01)
	if got := adminHarness.authorize.snapshot(); len(got) != 2 || got[1].Object != authzObjectToolApprovals || got[1].Action != authzActionResolve {
		t.Fatalf("approval deny route did not take its declared authorization decision: %+v", got)
	}
	if got := call("demo/read", "account 33"); got.Code != http.StatusOK {
		t.Fatalf("third sequence first call status=%d body=%s", got.Code, got.Body.String())
	}
	timeoutPending = call("demo/write", "send account 33")
	if !strings.Contains(timeoutPending.Body.String(), "approval_pending") {
		t.Fatalf("timeout fixture was not held: %s", timeoutPending.Body.String())
	}
	approvalNow = approvalNow.Add(2 * time.Second)
	timedOut := call("demo/write", "send account 33")
	if timedOut.Code != http.StatusOK || !strings.Contains(timedOut.Body.String(), `"isError":true`) || strings.Contains(timedOut.Body.String(), "approval_pending") {
		t.Fatalf("expired approval was not denied: status=%d body=%s", timedOut.Code, timedOut.Body.String())
	}
	if upstreamCalls != 4 {
		t.Fatalf("upstream tool calls=%d, want two reads, approved write, and third read only", upstreamCalls)
	}
}

func TestMCPGatewayRedactionBoundsFailClosed(t *testing.T) {
	cfg := &Config{Models: map[string]ModelGroup{"default": {
		PIIFilter: PIIFilterConfig{Enabled: true, SecretsEnabled: true},
	}}}
	deep := any("value")
	for range maxFirewallArgumentDepth + 2 {
		deep = map[string]any{"nested": deep}
	}
	if _, err := mcpRedactMCPToolInput(mcpPIIFilter(cfg, "default"), map[string]any{"value": deep}); !errors.Is(err, errMCPRedactionBounds) {
		t.Fatalf("unbounded MCP input was accepted: %v", err)
	}
	if _, err := mcpRedactToolResult(mcpPIIFilter(cfg, "default"), []any{deep}); !errors.Is(err, errMCPRedactionBounds) {
		t.Fatalf("unbounded MCP result was accepted: %v", err)
	}
}

func TestMCPGatewayUpstreamFailureAndTimeoutAreGeneric(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timeoutMS int
		handler   http.HandlerFunc
	}{
		{
			name: "upstream error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream secret detail", http.StatusBadGateway)
			},
		},
		{
			name:      "timeout",
			timeoutMS: 10,
			handler: func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(100 * time.Millisecond)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(tc.handler)
			defer upstream.Close()
			cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.db"))
			cfg.MCP = MCPGatewayConfig{
				Enabled: true, TimeoutMS: tc.timeoutMS,
				Servers:  []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}},
				Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read"}, DenyByDefault: true}},
			}
			svc, err := newService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if !strings.Contains(w.Body.String(), "upstream MCP request failed") || strings.Contains(w.Body.String(), "upstream secret detail") {
				t.Fatalf("failure response leaked upstream detail: %s", w.Body.String())
			}
		})
	}
}

func TestMCPGatewayUsesCurrentUpstreamProtocolWhenConfigured(t *testing.T) {
	var upstreamError string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			upstreamError = "invalid request"
			http.Error(w, upstreamError, http.StatusBadRequest)
			return
		}
		params, _ := request["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if request["method"] != "tools/list" || r.Header.Get("MCP-Protocol-Version") != "2026-07-28" || r.Header.Get("Mcp-Method") != "tools/list" || r.Header.Get("Mcp-Session-Id") != "" || meta["io.modelcontextprotocol/protocolVersion"] != "2026-07-28" {
			upstreamError = "current protocol metadata mismatch"
			http.Error(w, upstreamError, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}})
	}))
	defer upstream.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.db"))
	cfg.MCP = MCPGatewayConfig{
		Enabled:  true,
		Servers:  []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}, ProtocolVersion: mcpVersionCurrent}},
		Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/echo"}, DenyByDefault: true}},
	}
	svc, err := newService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "demo/echo") || upstreamError != "" {
		t.Fatalf("gateway current-upstream request status=%d body=%s upstream_error=%s", w.Code, w.Body.String(), upstreamError)
	}
}

func TestMCPGatewayRejectsOversizedAndBatchRequests(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	}))
	defer upstream.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.MCP = MCPGatewayConfig{Enabled: true, Servers: []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}}, Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read"}}}}
	svc, err := newService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for name, body := range map[string]string{
		"oversized": `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` + strings.Repeat("x", mcpMaxRequestBytes) + `"}}`,
		"batch":     `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"error"`) {
				t.Fatalf("invalid request status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid JSON-RPC requests reached upstream %d times", upstreamCalls)
	}
}

func TestMCPGatewayRejectsOversizedResponseAndRedirect(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "response over 8 MiB", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", mcpMaxResponseBytes+1))
		}},
		{name: "upstream 3xx", handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://other.example.test/steal", http.StatusFound)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(tc.handler)
			defer upstream.Close()
			cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
			cfg.MCP = MCPGatewayConfig{Enabled: true, Servers: []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}}, Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read"}}}}
			svc, err := newService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "upstream MCP request failed") || strings.Contains(w.Body.String(), strings.Repeat("x", 256)) {
				t.Fatalf("unsafe upstream response status=%d body prefix=%q", w.Code, w.Body.String()[:min(w.Body.Len(), 256)])
			}
		})
	}
}

func TestMCPProjectAndModelGroupPoliciesThroughHandler(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "policy-session")
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": mcpVersionLegacy}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			upstreamCalls++
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{}}})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{}}})
		}
	}))
	defer upstream.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.Callers[0].Allow = []string{"default"}
	cfg.MCP = MCPGatewayConfig{
		Enabled: true,
		Servers: []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}},
		Policies: []MCPPolicyConfig{
			{Project: "METRUM_Insights", Tools: []string{"demo/read"}, DenyByDefault: true},
			{ModelGroup: "default", Tools: []string{"demo/read"}, DenyByDefault: true},
		},
	}
	svc, err := newService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	request := func(model string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo/read","arguments":{}}}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		if model != "" {
			req.Header.Set("X-Router-Model-Group", model)
		}
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	if got := request("default"); got.Code != http.StatusOK {
		t.Fatalf("project + model-group policy status=%d body=%s", got.Code, got.Body.String())
	}
	if got := request("other"); !strings.Contains(got.Body.String(), "tool denied by policy") {
		t.Fatalf("caller-unallowed model group was not denied: status=%d body=%s", got.Code, got.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls=%d, want one allowed invocation", upstreamCalls)
	}
}

func TestMCPHandlerAppliesPerCallerRPM(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		upstreamCalls++
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "initialize":
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": mcpVersionLegacy}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{}}})
		}
	}))
	defer upstream.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.MCP = MCPGatewayConfig{Enabled: true, RPM: 1, Servers: []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}}, Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read"}}}}
	svc, err := newService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		if i == 0 && w.Code != http.StatusOK {
			t.Fatalf("first MCP request status=%d body=%s", w.Code, w.Body.String())
		}
		if i == 1 && (w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "limit exceeded")) {
			t.Fatalf("second MCP request status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if upstreamCalls != 3 {
		t.Fatalf("forwarded upstream requests=%d; second request should not reach upstream", upstreamCalls)
	}
	svc.quota.saveFault = fmt.Errorf("test persistence failure")
	svc.quota.callers["alice"].reqTimes = nil
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "could not be admitted") {
		t.Fatalf("quota persistence failure status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMCPHandlerAppliesPerCallerAndPerToolSpendLimits(t *testing.T) {
	toolCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "initialize":
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": mcpVersionLegacy}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			toolCalls++
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{}}})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{}}})
		}
	}))
	defer upstream.Close()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg.MCP = MCPGatewayConfig{
		Enabled: true, RPM: 10,
		Servers:  []MCPServerConfig{{Name: "demo", URL: upstream.URL, AllowHosts: []string{"127.0.0.1"}}},
		Policies: []MCPPolicyConfig{{Caller: "alice", Tools: []string{"demo/read"}}},
		Limits: []MCPToolLimitConfig{
			{Caller: "alice", DailyUSD: .20, CostPerCallUSD: .15},
			{Caller: "alice", Tool: "demo/read", DailyUSD: .20, CostPerCallUSD: .15},
		},
	}
	svc, err := newService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo/read","arguments":{}}}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	if first := call(); first.Code != http.StatusOK {
		t.Fatalf("first tool call status=%d body=%s", first.Code, first.Body.String())
	}
	if second := call(); second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), "limit exceeded") {
		t.Fatalf("tool spend limit status=%d body=%s", second.Code, second.Body.String())
	}
	if toolCalls != 1 {
		t.Fatalf("upstream tool calls=%d, want one", toolCalls)
	}
}
