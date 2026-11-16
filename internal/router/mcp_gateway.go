// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	mcpMaxRequestBytes  = 2 << 20
	mcpMaxResponseBytes = 8 << 20
	mcpVersionLegacy    = "2025-11-25"
	mcpVersionCurrent   = "2026-07-28"
)

func supportedMCPProtocolVersion(version string) bool {
	switch strings.TrimSpace(version) {
	case "2025-06-18", mcpVersionLegacy, mcpVersionCurrent:
		return true
	default:
		return false
	}
}

func negotiateMCPProtocolVersion(requested string) string {
	if supportedMCPProtocolVersion(requested) {
		return requested
	}
	return mcpVersionCurrent
}

func (s *Service) mcpRuntimeConfig() *Config {
	if s == nil {
		return nil
	}
	if s.configSnapshot != nil {
		return s.configSnapshot.Get()
	}
	return s.cfg
}

func matchingMCPToolLimits(limits []MCPToolLimitConfig, caller, tool string) []MCPToolLimitConfig {
	caller = strings.TrimSpace(caller)
	tool = strings.TrimSpace(tool)
	matched := make([]MCPToolLimitConfig, 0, 2)
	for _, limit := range limits {
		limit.Caller = strings.TrimSpace(limit.Caller)
		limit.Tool = strings.TrimSpace(limit.Tool)
		if limit.Caller == caller && (limit.Tool == "" || limit.Tool == tool) {
			matched = append(matched, limit)
		}
	}
	return matched
}

func mcpToolResultIsError(result any) bool {
	value, ok := result.(map[string]any)
	if !ok {
		return false
	}
	isError, _ := value["isError"].(bool)
	return isError
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

func validateMCPGateway(cfg MCPGatewayConfig, requireCredentials bool) error {
	if !cfg.Enabled {
		if len(cfg.Servers) > 0 || len(cfg.Policies) > 0 || len(cfg.Limits) > 0 || cfg.Path != "" || cfg.TimeoutMS != 0 || cfg.RPM != 0 {
			return fmt.Errorf("mcp fields require mcp.enabled")
		}
		return nil
	}
	if cfg.TimeoutMS < 0 || cfg.TimeoutMS > 300000 {
		return fmt.Errorf("mcp.timeout_ms must be zero or between 1 and 300000")
	}
	if cfg.RPM < 1 || cfg.RPM > 10000 {
		return fmt.Errorf("mcp.rpm must be between 1 and 10000")
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = "/mcp"
	}
	if path != "/mcp" {
		return fmt.Errorf("mcp.path must be /mcp")
	}
	if len(cfg.Servers) == 0 {
		return fmt.Errorf("mcp.servers must contain at least one server")
	}
	if len(cfg.Servers) > 32 || len(cfg.Policies) > 1000 || len(cfg.Limits) > 1000 {
		return fmt.Errorf("mcp servers, policies, or limits exceed configured bounds")
	}
	servers := make(map[string]bool, len(cfg.Servers))
	for _, server := range cfg.Servers {
		name := strings.TrimSpace(server.Name)
		if name == "" || strings.Contains(name, "/") || servers[name] {
			return fmt.Errorf("mcp server names must be unique non-empty names without slash")
		}
		servers[name] = true
		u, err := url.Parse(strings.TrimSpace(server.URL))
		if err != nil || u.Fragment != "" || u.RawQuery != "" {
			return fmt.Errorf("mcp server %q url must not include a query or fragment", name)
		}
		if strings.EqualFold(u.Scheme, "http") && !egressHostIsTrustedLocal(u.Hostname()) {
			return fmt.Errorf("mcp server %q: cleartext HTTP is allowed only for loopback hosts", name)
		}
		if err := validateEgressURL(u, server.AllowHosts, server.AllowHTTP, "MCP server "+name); err != nil {
			return fmt.Errorf("mcp server %q: %w", name, err)
		}
		if env := strings.TrimSpace(server.CredentialEnv); env != "" {
			if !validEnvName(env) || !strings.HasPrefix(env, "ROUTER_MCP_") {
				return fmt.Errorf("mcp server %q credential_env must use the ROUTER_MCP_ prefix", name)
			}
			if value, ok := os.LookupEnv(env); requireCredentials && (!ok || strings.TrimSpace(value) == "") {
				return fmt.Errorf("mcp server %q credential_env %q is unset", name, env)
			}
		}
		if version := strings.TrimSpace(server.ProtocolVersion); version != "" && version != mcpVersionLegacy && version != mcpVersionCurrent {
			return fmt.Errorf("mcp server %q protocol_version must be %q or %q", name, mcpVersionLegacy, mcpVersionCurrent)
		}
	}
	seenLimits := make(map[string]struct{}, len(cfg.Limits))
	for _, limit := range cfg.Limits {
		caller := strings.TrimSpace(limit.Caller)
		if caller == "" || limit.RPM < 0 || limit.RPM > 100000 || limit.CostPerCallUSD < 0 || limit.HourlyUSD < 0 || limit.DailyUSD < 0 ||
			math.IsNaN(limit.CostPerCallUSD) || math.IsInf(limit.CostPerCallUSD, 0) || math.IsNaN(limit.HourlyUSD) || math.IsInf(limit.HourlyUSD, 0) || math.IsNaN(limit.DailyUSD) || math.IsInf(limit.DailyUSD, 0) {
			return fmt.Errorf("mcp limit requires a caller and finite non-negative limits")
		}
		if (limit.HourlyUSD > 0 || limit.DailyUSD > 0) && limit.CostPerCallUSD <= 0 {
			return fmt.Errorf("mcp limit with spend ceilings requires cost_per_call_usd")
		}
		if limit.RPM == 0 && limit.HourlyUSD == 0 && limit.DailyUSD == 0 {
			return fmt.Errorf("mcp limit must set a rate or spend ceiling")
		}
		if tool := strings.TrimSpace(limit.Tool); tool != "" {
			parts := strings.SplitN(tool, "/", 2)
			if len(parts) != 2 || !servers[parts[0]] || strings.TrimSpace(parts[1]) == "" {
				return fmt.Errorf("mcp limit tool must use a configured server/name")
			}
		}
		limitKey := caller + "\x00" + strings.TrimSpace(limit.Tool)
		if _, exists := seenLimits[limitKey]; exists {
			return fmt.Errorf("mcp limit for caller and tool is duplicated")
		}
		seenLimits[limitKey] = struct{}{}
	}
	for _, policy := range cfg.Policies {
		if strings.TrimSpace(policy.Caller) == "" && strings.TrimSpace(policy.Project) == "" && strings.TrimSpace(policy.ModelGroup) == "" {
			return fmt.Errorf("mcp policy must identify a caller, project, or model group")
		}
		for _, tool := range policy.Tools {
			parts := strings.SplitN(strings.TrimSpace(tool), "/", 2)
			if len(parts) != 2 || !servers[parts[0]] || strings.TrimSpace(parts[1]) == "" {
				return fmt.Errorf("mcp policy tool must use a configured server/name")
			}
		}
	}
	return nil
}

func writeMCPAdmissionError(w http.ResponseWriter, id json.RawMessage, status int, message string) {
	if status < 400 || status > 599 {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	code := -32000
	if status == http.StatusTooManyRequests {
		code = -32029
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func validateMCPPolicyReferences(cfg MCPGatewayConfig, callers []CallerConfig, projects []ProjectConfig, models map[string]ModelGroup) error {
	if !cfg.Enabled {
		return nil
	}
	callerIDs := make(map[string]bool, len(callers))
	projectIDs := make(map[string]bool, len(projects))
	for _, caller := range callers {
		callerIDs[caller.ID] = true
		if project := strings.TrimSpace(caller.Project); project != "" {
			projectIDs[normalizeAccountID(project)] = true
		}
	}
	for _, project := range projects {
		projectIDs[normalizeAccountID(project.ID)] = true
	}
	for _, policy := range cfg.Policies {
		if policy.Caller != "" && !callerIDs[policy.Caller] {
			return fmt.Errorf("mcp policy references unknown caller %q", policy.Caller)
		}
		if policy.Project != "" && !projectIDs[normalizeAccountID(policy.Project)] {
			return fmt.Errorf("mcp policy references unknown project %q", policy.Project)
		}
		if policy.ModelGroup != "" {
			if _, ok := models[policy.ModelGroup]; !ok {
				return fmt.Errorf("mcp policy references unknown model group %q", policy.ModelGroup)
			}
		}
	}
	for _, limit := range cfg.Limits {
		if !callerIDs[limit.Caller] {
			return fmt.Errorf("mcp limit references unknown caller %q", limit.Caller)
		}
	}
	return nil
}

func (s *Service) handleMCP(w http.ResponseWriter, r *http.Request) {
	cfg := s.mcpRuntimeConfig()
	if cfg == nil || !cfg.MCP.Enabled {
		http.NotFound(w, r)
		return
	}
	caller, _, err := s.authenticate(r.Header.Get("Authorization"), r.Header.Get("X-API-Key"))
	if err != nil || caller == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "authentication required"}})
		return
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, parseErr := url.Parse(origin)
		if parseErr != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mcpMaxRequestBytes)
	var request mcpRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil || request.JSONRPC != "2.0" || request.Method == "" {
		writeMCPError(w, nil, -32700, "invalid JSON-RPC request")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeMCPError(w, request.ID, -32600, "one JSON-RPC request is required")
		return
	}
	protocolVersion := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
	if protocolVersion == "" {
		protocolVersion = mcpVersionLegacy
	}
	if protocolVersion != mcpVersionLegacy && protocolVersion != mcpVersionCurrent && protocolVersion != "2025-06-18" {
		writeMCPHTTPError(w, request.ID, -32042, "unsupported protocol version")
		return
	}
	if protocolVersion == mcpVersionCurrent && !validateMCPRequestHeaders(r, request, protocolVersion) {
		writeMCPHTTPError(w, request.ID, -32020, "MCP request headers do not match the JSON-RPC body")
		return
	}
	modelGroup := strings.TrimSpace(r.Header.Get("X-Router-Model-Group"))
	modelGroupDenied := modelGroup != "" && !caller.allow[modelGroup]
	if request.Method == "initialize" {
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(request.Params) != 0 && json.Unmarshal(request.Params, &params) != nil {
			writeMCPError(w, request.ID, -32602, "invalid initialize parameters")
			return
		}
		if params.ProtocolVersion != "" {
			protocolVersion = negotiateMCPProtocolVersion(params.ProtocolVersion)
		}
	}
	var callParams struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if request.Method == "tools/call" && (json.Unmarshal(request.Params, &callParams) != nil || callParams.Name == "") {
		writeMCPError(w, request.ID, -32602, "invalid tools/call parameters")
		return
	}
	quotaAdmission := s.quota.AdmitMCP(caller, cfg.MCP.RPM)
	if !quotaAdmission.OK {
		if request.Method == "tools/call" {
			auditID, auditErr := s.beginMCPToolAudit(caller, callParams.Name, callParams.Arguments)
			if auditErr != nil {
				writeMCPError(w, request.ID, -32000, "MCP audit store unavailable")
				return
			}
			if auditErr = s.finishMCPToolAudit(auditID, time.Now(), "limited"); auditErr != nil {
				writeMCPError(w, request.ID, -32000, "MCP audit store unavailable")
				return
			}
		}
		if quotaAdmission.RetryAfter != "" {
			w.Header().Set("Retry-After", quotaAdmission.RetryAfter)
		}
		message := "MCP request limit exceeded"
		if quotaAdmission.Status != http.StatusTooManyRequests {
			message = "MCP request could not be admitted"
		}
		writeMCPAdmissionError(w, request.ID, quotaAdmission.Status, message)
		return
	}
	defer s.quota.Release(caller)
	if request.Method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch request.Method {
	case "initialize":
		protocolVersion = negotiateMCPProtocolVersion(protocolVersion)
		result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": "metrum-router", "version": "5.0.0"},
		}
	case "tools/list":
		if modelGroupDenied {
			writeMCPError(w, request.ID, -32003, "model group denied by caller policy")
			return
		}
		result, err = s.mcpListTools(r.Context(), cfg, caller, modelGroup)
	case "tools/call":
		started := time.Now()
		auditID, auditErr := s.beginMCPToolAudit(caller, callParams.Name, callParams.Arguments)
		if auditErr != nil {
			writeMCPError(w, request.ID, -32000, "MCP audit store unavailable")
			return
		}
		if modelGroupDenied || !s.mcpToolAllowedWithConfig(cfg, caller, modelGroup, callParams.Name) {
			if auditErr := s.finishMCPToolAudit(auditID, started, "denied"); auditErr != nil {
				writeMCPError(w, request.ID, -32000, "MCP audit store unavailable")
				return
			}
			writeMCPError(w, request.ID, -32003, "tool denied by policy")
			return
		}
		limitAdmission := s.quota.AdmitMCPTool(caller.cfg.ID, callParams.Name, matchingMCPToolLimits(cfg.MCP.Limits, caller.cfg.ID, callParams.Name))
		if !limitAdmission.OK {
			if auditErr := s.finishMCPToolAudit(auditID, started, "limited"); auditErr != nil {
				writeMCPError(w, request.ID, -32000, "MCP audit store unavailable")
				return
			}
			if limitAdmission.RetryAfter != "" {
				w.Header().Set("Retry-After", limitAdmission.RetryAfter)
			}
			message := "MCP tool limit exceeded"
			if limitAdmission.Status != http.StatusTooManyRequests {
				message = "MCP tool call could not be admitted"
			}
			writeMCPAdmissionError(w, request.ID, limitAdmission.Status, message)
			return
		}
		result, err = s.mcpCallTool(r.Context(), cfg, callParams.Name, callParams.Arguments)
		resultStatus := "success"
		if err != nil || mcpToolResultIsError(result) {
			resultStatus = "error"
		}
		if auditErr = s.finishMCPToolAudit(auditID, started, resultStatus); auditErr != nil {
			writeMCPError(w, request.ID, -32000, "MCP audit store unavailable")
			return
		}
	default:
		writeMCPError(w, request.ID, -32601, "method not found")
		return
	}
	if err != nil {
		writeMCPError(w, request.ID, -32000, "upstream MCP request failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("MCP-Protocol-Version", protocolVersion)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func validateMCPRequestHeaders(r *http.Request, request mcpRequest, protocolVersion string) bool {
	if r.Header.Get("Mcp-Method") != request.Method {
		return false
	}
	var params map[string]any
	if json.Unmarshal(request.Params, &params) != nil {
		return false
	}
	meta, _ := params["_meta"].(map[string]any)
	if stringValue(meta["io.modelcontextprotocol/protocolVersion"]) != protocolVersion {
		return false
	}
	if request.Method != "tools/call" {
		return true
	}
	name := stringValue(params["name"])
	if name == "" {
		return false
	}
	headerName, ok := decodeMCPHeaderValue(r.Header.Get("Mcp-Name"))
	return ok && headerName == name
}

func decodeMCPHeaderValue(value string) (string, bool) {
	if strings.HasPrefix(value, "=?base64?") && strings.HasSuffix(value, "?=") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(value, "=?base64?"), "?="))
		return string(decoded), err == nil
	}
	return value, value != ""
}

func (s *Service) mcpListTools(ctx context.Context, cfg *Config, caller *callerRuntime, modelGroup string) (map[string]any, error) {
	type serverResult struct {
		tools []mcpTool
		err   error
	}
	results := make([]serverResult, len(cfg.MCP.Servers))
	var wg sync.WaitGroup
	for i, server := range cfg.MCP.Servers {
		wg.Add(1)
		go func(index int, server MCPServerConfig) {
			defer wg.Done()
			var upstream struct {
				Tools []mcpTool `json:"tools"`
			}
			if err := s.mcpUpstream(ctx, cfg, server, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}}, &upstream); err != nil {
				results[index].err = err
				return
			}
			results[index].tools = upstream.Tools
		}(i, server)
	}
	wg.Wait()
	var tools []mcpTool
	failed := 0
	for i, server := range cfg.MCP.Servers {
		if results[i].err != nil {
			failed++
			continue
		}
		for _, tool := range results[i].tools {
			tool.Name = server.Name + "/" + tool.Name
			if s.mcpToolAllowedWithConfig(cfg, caller, modelGroup, tool.Name) {
				tools = append(tools, tool)
			}
		}
	}
	if failed == len(cfg.MCP.Servers) {
		return nil, fmt.Errorf("all MCP servers failed")
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return map[string]any{"tools": tools}, nil
}

func (s *Service) mcpToolAllowed(caller *callerRuntime, modelGroup, name string) bool {
	return s.mcpToolAllowedWithConfig(s.mcpRuntimeConfig(), caller, modelGroup, name)
}

func (s *Service) mcpToolAllowedWithConfig(cfg *Config, caller *callerRuntime, modelGroup, name string) bool {
	if caller == nil || cfg == nil {
		return false
	}
	matched := false
	for i := range cfg.MCP.Policies {
		policy := &cfg.MCP.Policies[i]
		if policy.Caller != "" && policy.Caller != caller.cfg.ID {
			continue
		}
		if policy.Project != "" && normalizeAccountID(policy.Project) != caller.project {
			continue
		}
		if policy.ModelGroup != "" && policy.ModelGroup != modelGroup {
			continue
		}
		matched = true
		if policy.DenyByDefault || len(policy.Tools) != 0 {
			allowed := false
			for _, tool := range policy.Tools {
				if tool == name {
					allowed = true
					break
				}
			}
			if !allowed {
				return false
			}
		}
	}
	return matched
}

func (s *Service) mcpCallTool(ctx context.Context, cfg *Config, qualifiedName string, arguments map[string]any) (map[string]any, error) {
	serverName, toolName, ok := strings.Cut(qualifiedName, "/")
	if !ok {
		return nil, fmt.Errorf("invalid tool name")
	}
	for _, server := range cfg.MCP.Servers {
		if server.Name != serverName {
			continue
		}
		var result map[string]any
		err := s.mcpUpstream(ctx, cfg, server, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": toolName, "arguments": arguments},
		}, &result)
		return result, err
	}
	return nil, fmt.Errorf("unknown server")
}

func (s *Service) mcpUpstream(ctx context.Context, cfg *Config, server MCPServerConfig, request any, result any) error {
	timeout := cfg.MCP.TimeoutMS
	if timeout == 0 {
		timeout = 30000
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	protocolVersion := strings.TrimSpace(server.ProtocolVersion)
	if protocolVersion == "" {
		protocolVersion = mcpVersionLegacy
	}
	if protocolVersion == mcpVersionCurrent {
		request, err := withMCPRequestMetadata(request, protocolVersion)
		if err != nil {
			return err
		}
		return s.mcpPost(ctx, cfg, server, request, result, nil, protocolVersion)
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	var sessionID string
	initErr := s.mcpPost(ctx, cfg, server, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "metrum-router", "version": "5.0.0"},
		},
	}, &initialized, &sessionID, protocolVersion)
	if sessionID != "" {
		defer s.mcpCloseSession(ctx, cfg, server, sessionID, protocolVersion)
	}
	if initErr != nil {
		return initErr
	}
	if initialized.ProtocolVersion == "" {
		return fmt.Errorf("upstream initialization failed")
	}
	if !supportedMCPProtocolVersion(initialized.ProtocolVersion) {
		return fmt.Errorf("upstream selected unsupported protocol version")
	}
	if err := s.mcpPost(ctx, cfg, server, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil, &sessionID, initialized.ProtocolVersion); err != nil {
		return err
	}
	return s.mcpPost(ctx, cfg, server, request, result, &sessionID, initialized.ProtocolVersion)
}

func withMCPRequestMetadata(request any, protocolVersion string) (any, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	params, _ := envelope["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
		envelope["params"] = params
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    protocolVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]string{"name": "metrum-router", "version": "5.0.0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	return envelope, nil
}

func (s *Service) mcpPost(ctx context.Context, cfg *Config, server MCPServerConfig, request any, result any, sessionID *string, protocolVersion string) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	var requestType struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &requestType)
	if requestType.Method != "initialize" {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	if protocolVersion == mcpVersionCurrent {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
		req.Header.Set("Mcp-Method", requestType.Method)
		var envelope struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &envelope)
		if envelope.Params.Name != "" {
			req.Header.Set("Mcp-Name", envelope.Params.Name)
		}
	}
	if sessionID != nil && *sessionID != "" {
		req.Header.Set("Mcp-Session-Id", *sessionID)
	}
	if env := strings.TrimSpace(server.CredentialEnv); env != "" {
		if credential := os.Getenv(env); credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
	}
	timeout := cfg.MCP.TimeoutMS
	if timeout <= 0 {
		timeout = 30000
	}
	client := newMCPHTTPClient(time.Duration(timeout)*time.Millisecond, server)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if result == nil && response.StatusCode == http.StatusAccepted {
			return nil
		}
		return fmt.Errorf("upstream returned status %d", response.StatusCode)
	}
	if sessionID != nil && response.Header.Get("Mcp-Session-Id") != "" {
		*sessionID = response.Header.Get("Mcp-Session-Id")
	}
	limited := io.LimitReader(response.Body, mcpMaxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) > mcpMaxResponseBytes {
		return fmt.Errorf("invalid upstream response")
	}
	if result == nil {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		raw = mcpEventResponse(raw)
	}
	var envelope struct {
		Error  *json.RawMessage `json:"error"`
		Result json.RawMessage  `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Error != nil || len(envelope.Result) == 0 {
		return fmt.Errorf("invalid upstream response")
	}
	return json.Unmarshal(envelope.Result, result)
}

func (s *Service) mcpCloseSession(ctx context.Context, cfg *Config, server MCPServerConfig, sessionID, protocolVersion string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, server.URL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", sessionID)
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if env := strings.TrimSpace(server.CredentialEnv); env != "" {
		if credential := os.Getenv(env); credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
	}
	timeout := cfg.MCP.TimeoutMS
	if timeout <= 0 {
		timeout = 30000
	}
	client := newMCPHTTPClient(time.Duration(timeout)*time.Millisecond, server)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return
	}
	_ = response.Body.Close()
}

func newMCPHTTPClient(timeout time.Duration, server MCPServerConfig) *http.Client {
	return newEgressHTTPClientWithOptions(egressHTTPClientOptions{
		Timeout: timeout, AllowHosts: server.AllowHosts, AllowHTTP: server.AllowHTTP, Label: "MCP server " + server.Name,
		// A proxy would see the Authorization header and bypass destination-IP
		// checks for tunneled HTTPS requests. MCP credentials go directly to the
		// allowlisted upstream host.
		Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
	})
}

func mcpEventResponse(raw []byte) []byte {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		var envelope map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &envelope) == nil && (envelope["result"] != nil || envelope["error"] != nil) {
			return []byte(data)
		}
	}
	return nil
}

func writeMCPError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func writeMCPHTTPError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}
