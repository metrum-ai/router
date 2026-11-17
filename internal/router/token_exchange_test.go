// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestTokenExchangeScopedJWTAndExpiry(t *testing.T) {
	auth, key := runtimeJWTIssuerFixture(t, "caller-a", "federated")
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: "caller-a", HumanOwner: "alice@example.test", AllowedGroups: []string{"small"}, MaxSpendUSD: 0.25, MaxTokens: 500, AllowedTools: []string{"search"}}
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Allow: []string{"small"}}, allow: map[string]bool{"small": true}, keyStatus: accountStatusActive, ownerUserStatus: accountStatusActive, projectStatus: accountStatusActive, membershipStatus: accountStatusActive}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: time.Minute, issuers: map[string]*tokenExchangeIssuer{auth.cfg.Issuer: {auth: auth, taskClaim: "task_id", grants: map[string]tokenExchangeGrant{"sub\x00automation": {config: grant}}}}, callers: map[string]*callerRuntime{"caller-a": caller}}
	claims := runtimeJWTClaims(auth.cfg.Issuer)
	claims["task_id"] = "task-123"
	access, expires, err := rt.exchange(context.Background(), machineSignedJWT(t, key, "known", claims), []string{"small"}, "task-123")
	if err != nil || expires != 60 {
		t.Fatalf("exchange failed: expires=%d err=%v", expires, err)
	}
	got, id, mission, err := rt.verifyAccessToken(access)
	if err != nil || got != caller || !strings.HasPrefix(id, "mission_") || mission.TaskID != "task-123" || mission.HumanOwner != grant.HumanOwner || mission.MaxTokens != 500 {
		t.Fatalf("scope/attribution mismatch: %#v %q %#v %v", got, id, mission, err)
	}
	if _, _, err := rt.exchange(context.Background(), machineSignedJWT(t, key, "known", claims), []string{"admin"}, "task-123"); err == nil {
		t.Fatal("out-of-scope model group accepted")
	}
	ctx := &requestContext{caller: caller, mission: mission}
	if !ctx.callerCanUseGroup("small") || ctx.callerCanUseGroup("admin") {
		t.Fatal("mission model restriction failed")
	}
	oversized := &IRRequest{MaxTokens: 501, MaxTokensField: "max_tokens", Raw: map[string]any{}}
	if !missionTokenLimit(ctx, oversized) || oversized.MaxTokens >= 501 || oversized.Raw["max_tokens"] != oversized.MaxTokens {
		t.Fatalf("oversized request was not clamped to remaining budget: %+v", oversized)
	}
	limited := &IRRequest{Raw: map[string]any{}}
	if !missionTokenLimit(ctx, limited) {
		t.Fatal("default mission token budget rejected")
	}
	if limited.Raw["max_tokens"] != limited.MaxTokens {
		t.Fatalf("upstream max_tokens=%v, want remaining limit %d", limited.Raw["max_tokens"], limited.MaxTokens)
	}
}

func TestTokenExchangeRequestRejectsWrongGrant(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=bad&subject_token=x&subject_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Ajwt&task_id=t"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := parseTokenExchangeRequest(r); err == nil {
		t.Fatal("wrong grant accepted")
	}
}

func TestTokenExchangeUnreachableIssuerDoesNotBlockStartup(t *testing.T) {
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Allow: []string{"small"}}, allow: map[string]bool{"small": true}, keyStatus: accountStatusActive, ownerUserStatus: accountStatusActive, projectStatus: accountStatusActive, membershipStatus: accountStatusActive}
	cfg := TokenExchangeConfig{Enabled: true, SigningKeyEnv: "MISSION_KEY", DefaultTTL: "1m", Issuers: []TokenExchangeIssuerConfig{{
		IssuerURL: "https://issuer.example.invalid", Audience: "router-exchange", AllowedJWKSHosts: []string{"issuer.example.invalid"},
		Subjects: []TokenExchangeSubjectGrant{{Claim: "sub", Value: "automation", CallerID: "caller-a", HumanOwner: "team", AllowedGroups: []string{"small"}, MaxSpendUSD: 1, MaxTokens: 100}},
	}}}
	rt, err := newTokenExchangeRuntime(cfg, map[string]*callerRuntime{"caller-a": caller}, func(string) (string, bool) { return strings.Repeat("k", 32), true }, &http.Client{Timeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("unreachable discovery blocked data-plane startup: %v", err)
	}
	svc := &Service{mux: http.NewServeMux(), tokenExchange: rt}
	svc.mux.HandleFunc("POST /oauth/token", svc.handleTokenExchange)
	claims := map[string]any{"iss": "https://issuer.example.invalid", "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "task_id": "task"}
	form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {machineSignedJWTType(t, machineJWTKey(t), "kid", jose.ES256, "JWT", claims)}, "subject_token_type": {jwtSubjectTokenType}, "scope": {"small"}}
	r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable issuer status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMissionBudgetAccumulatesAtRequestFinish(t *testing.T) {
	rt := &tokenExchangeRuntime{}
	claims := &missionTokenClaims{Claims: jwt.Claims{ID: requestID(), Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}, MaxTokens: 10, MaxSpendUSD: .1}
	if !rt.admitMission(claims, 8, .04) {
		t.Fatal("first mission request not admitted")
	}
	if !rt.admitMission(claims, 2, .02) {
		t.Fatal("concurrent request within reserved budget rejected")
	}
	rt.settleMission(claims, 8, .04, Usage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7}, .07)
	rt.settleMission(claims, 2, .02, Usage{TotalTokens: 2}, .02)
	if !rt.admitMission(claims, 1, .01) {
		t.Fatal("request within remaining budget rejected")
	}
	rt.settleMission(claims, 1, .01, Usage{TotalTokens: 1}, .01)
	if rt.admitMission(claims, 1, .01) {
		t.Fatal("cumulative token or spend cap exceeded")
	}
}

func TestMissionBudgetKeyIsGrantScopedAcrossReexchange(t *testing.T) {
	rt := &tokenExchangeRuntime{budgets: make(map[string]*missionBudget)}
	first := &missionTokenClaims{Claims: jwt.Claims{ID: "token-one", Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}, GrantID: "issuer-subject-grant", MaxTokens: 10, MaxSpendUSD: .1}
	if !rt.admitMission(first, 10, .1) {
		t.Fatal("first exchange budget not admitted")
	}
	rt.settleMission(first, 10, .1, Usage{TotalTokens: 10}, .1)
	refreshed := *first
	refreshed.ID = "token-two"
	if rt.admitMission(&refreshed, 1, .001) {
		t.Fatal("re-exchange reset the grant's cumulative mission cap")
	}
	if len(rt.budgets) != 1 {
		t.Fatalf("budget table has %d entries for one configured grant", len(rt.budgets))
	}
}

func TestMissionBudgetSurvivesTokenExpiryAndLateSettlement(t *testing.T) {
	rt := &tokenExchangeRuntime{budgets: make(map[string]*missionBudget)}
	expires := time.Now().Add(25 * time.Millisecond)
	oldToken := &missionTokenClaims{Claims: jwt.Claims{ID: "old-token", Expiry: jwt.NewNumericDate(expires)}, GrantID: "issuer-subject-grant", MaxTokens: 10, MaxSpendUSD: 1}
	if !rt.admitMission(oldToken, 6, .6) {
		t.Fatal("request reservation before token expiry was rejected")
	}
	time.Sleep(time.Until(expires) + 5*time.Millisecond)
	// The request stream may finish after the bearer expires. Its settlement
	// must still attach to the grant budget.
	rt.settleMission(oldToken, 6, .6, Usage{TotalTokens: 6}, .6)
	refreshed := *oldToken
	refreshed.ID = "new-token"
	refreshed.Expiry = jwt.NewNumericDate(time.Now().Add(time.Minute))
	remainingTokens, remainingSpend := rt.remainingMission(&refreshed)
	if remainingTokens != 4 || math.Abs(remainingSpend-.4) > 1e-9 {
		t.Fatalf("post-expiry settled budget remaining=(%d,%.3f), want (4,.4)", remainingTokens, remainingSpend)
	}
	if !rt.admitMission(&refreshed, 4, .4) {
		t.Fatal("request using exactly the remaining post-expiry budget was rejected")
	}
	if rt.admitMission(&refreshed, 1, .001) {
		t.Fatal("post-expiry re-exchange exceeded the preserved token or spend cap")
	}
}

func TestTokenExchangeReexchangeKeepsConfiguredGrantBudget(t *testing.T) {
	auth, key := runtimeJWTIssuerFixture(t, "caller-a", "federated")
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: "caller-a", HumanOwner: "team", AllowedGroups: []string{"small"}, MaxSpendUSD: .1, MaxTokens: 3}
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Allow: []string{"small"}}, allow: map[string]bool{"small": true}, keyStatus: accountStatusActive, ownerUserStatus: accountStatusActive, projectStatus: accountStatusActive, membershipStatus: accountStatusActive}
	issuer := &tokenExchangeIssuer{auth: auth, taskClaim: "task_id", grants: map[string]tokenExchangeGrant{"sub\x00automation": {config: grant}}}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: time.Minute, issuers: map[string]*tokenExchangeIssuer{auth.cfg.Issuer: issuer}, callers: map[string]*callerRuntime{"caller-a": caller}, budgets: make(map[string]*missionBudget)}
	claims := runtimeJWTClaims(auth.cfg.Issuer)
	claims["task_id"] = "task-one"
	subject := machineSignedJWTType(t, key, "known", jose.ES256, "JWT", claims)
	firstToken, _, err := rt.exchange(context.Background(), subject, []string{"small"}, "task-one")
	if err != nil {
		t.Fatal(err)
	}
	_, _, first, err := rt.verifyAccessToken(firstToken)
	if err != nil {
		t.Fatal(err)
	}
	if !rt.admitMission(first, 3, .1) {
		t.Fatal("initial request did not reserve the configured cap")
	}
	rt.settleMission(first, 3, .1, Usage{TotalTokens: 3}, .1)
	secondToken, _, err := rt.exchange(context.Background(), subject, []string{"small"}, "task-one")
	if err != nil {
		t.Fatal(err)
	}
	_, _, second, err := rt.verifyAccessToken(secondToken)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.GrantID != second.GrantID {
		t.Fatal("refresh must mint a new token while retaining the same grant budget identity")
	}
	if rt.admitMission(second, 1, .001) {
		t.Fatal("re-exchanging the workload JWT reset its cumulative budget")
	}
}

func TestTokenExchangeRateLimitsPerConfiguredSubject(t *testing.T) {
	auth, key := runtimeJWTIssuerFixture(t, "caller-a", "federated")
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: "caller-a", HumanOwner: "team", AllowedGroups: []string{"small"}, MaxSpendUSD: 1, MaxTokens: 10}
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Allow: []string{"small"}}, allow: map[string]bool{"small": true}, keyStatus: accountStatusActive, ownerUserStatus: accountStatusActive, projectStatus: accountStatusActive, membershipStatus: accountStatusActive}
	issuer := &tokenExchangeIssuer{auth: auth, taskClaim: "task_id", grants: map[string]tokenExchangeGrant{"sub\x00automation": {config: grant}}}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: time.Minute, issuers: map[string]*tokenExchangeIssuer{auth.cfg.Issuer: issuer}, callers: map[string]*callerRuntime{"caller-a": caller}}
	claims := runtimeJWTClaims(auth.cfg.Issuer)
	claims["task_id"] = "task-rate"
	subject := machineSignedJWTType(t, key, "known", jose.ES256, "JWT", claims)
	for i := 0; i < tokenExchangeRPM; i++ {
		if _, _, err := rt.exchange(context.Background(), subject, []string{"small"}, "task-rate"); err != nil {
			t.Fatalf("exchange %d failed before rate limit: %v", i+1, err)
		}
	}
	if _, _, err := rt.exchange(context.Background(), subject, []string{"small"}, "task-rate"); !errors.Is(err, errTokenExchangeRateLimited) {
		t.Fatalf("exchange beyond subject rate limit error=%v", err)
	}
}

func TestMissionTaskIDRequiresSignedBoundedASCIIClaim(t *testing.T) {
	for _, task := range []string{"task_42", "a:b.c-d", strings.Repeat("a", maxTaskIDBytes)} {
		if !validMissionTaskID(task) {
			t.Fatalf("valid task id rejected: %q", task)
		}
	}
	for _, task := range []string{"", " task", "task/1", "task\n1", strings.Repeat("a", maxTaskIDBytes+1), "-task"} {
		if validMissionTaskID(task) {
			t.Fatalf("invalid task id accepted: %q", task)
		}
	}
}

func TestMissionTokenEndToEndUsageAndScopes(t *testing.T) {
	var upstreamCalls atomic.Int64
	maxTokensForwarded := make(chan int, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if value, ok := payload["max_tokens"].(float64); ok {
			maxTokensForwarded <- int(value)
		}
		maxTokens, _ := payload["max_tokens"].(float64)
		completionTokens := min(int(maxTokens), 3)
		_, _ = fmt.Fprintf(w, `{"id":"completion","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":%d,"total_tokens":%d}}`, completionTokens, 3+completionTokens)
	}))
	defer upstream.Close()
	var enabled = true
	var svc *Service
	honorsMaxTokens := true
	svc = nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{{Provider: "native", Model: "up", ContextTokens: 4096, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 10, HonorsMaxTokens: &honorsMaxTokens, RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-chat"}, ValidationStatus: "passed", MaxEstimatedInputTokens: 100, MinRequestedOutputTokens: 1, MaxRequestedOutputTokens: 100}, InputModalities: []string{"text"}, OutputModalities: []string{"text"}}}, func(cfg *Config) {
		cfg.Server.UsageDB.Enable = &enabled
		cfg.Server.UsageDB.Path = filepath.Join(t.TempDir(), "mission-usage.sqlite")
	})
	caller, _ := svc.callerAuth.lookup(testToken)
	if caller == nil {
		t.Fatal("test caller unavailable")
	}
	target := svc.cfg.Models["native-stream"].Targets[0]
	probe := &IRRequest{MaxTokens: 1, MaxTokensField: "max_tokens", Raw: map[string]any{"model": "native-stream", "max_tokens": float64(1), "messages": []any{map[string]any{"role": "user", "content": "x"}}}}
	if reasons := svc.targetEligibilityReasons(target, probe, "openai-chat", "openai-chat", svc.requestTokenEstimateForSelection(nil, probe, "openai-chat")); len(reasons) != 0 {
		t.Fatalf("test target is not eligible: %v", reasons)
	}
	svc.cfg.Projects = []ProjectConfig{{ID: "metrum-insights", ForbidLongLivedKeys: true}}
	auth, subjectKey := runtimeJWTIssuerFixture(t, caller.cfg.ID, "federated")
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: caller.cfg.ID, HumanOwner: "owner-team", AllowedGroups: []string{"native-stream"}, MaxSpendUSD: .00024, MaxTokens: 12, AllowedTools: []string{"search"}}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: time.Minute, issuers: map[string]*tokenExchangeIssuer{auth.cfg.Issuer: {auth: auth, taskClaim: "task_id", grants: map[string]tokenExchangeGrant{"sub\x00automation": {config: grant}}}}, callers: map[string]*callerRuntime{caller.cfg.ID: caller}}
	svc.tokenExchange = rt
	subjectClaims := runtimeJWTClaims(auth.cfg.Issuer)
	subjectClaims["task_id"] = "mission-task-4"
	mission, _, err := rt.exchange(context.Background(), machineSignedJWTType(t, subjectKey, "known", jose.ES256, "JWT", subjectClaims), []string{"native-stream"}, "mission-task-4")
	if err != nil {
		t.Fatal(err)
	}
	postToken := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	post := func(body string) *httptest.ResponseRecorder { return postToken(mission, body) }
	tamperedParts := strings.Split(mission, ".")
	if len(tamperedParts) != 3 {
		t.Fatal("issued mission token is not compact JWT")
	}
	tamperedParts[2] = "A" + tamperedParts[2][1:]
	if tamperedParts[2] == strings.Split(mission, ".")[2] {
		tamperedParts[2] = "B" + tamperedParts[2][1:]
	}
	tampered := postToken(strings.Join(tamperedParts, "."), `{"model":"native-stream","messages":[{"role":"user","content":"x"}]}`)
	if tampered.Code != http.StatusUnauthorized {
		t.Fatalf("tampered mission token status=%d", tampered.Code)
	}
	_, _, claims, err := rt.verifyAccessToken(mission)
	if err != nil {
		t.Fatal(err)
	}
	claims.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Minute))
	expiredSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: rt.key}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := jwt.Signed(expiredSigner).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	expiredResponse := postToken(expired, `{"model":"native-stream","messages":[{"role":"user","content":"x"}]}`)
	if expiredResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expired mission token status=%d", expiredResponse.Code)
	}
	denied := post(`{"model":"native-stream","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"delete_all","parameters":{"type":"object"}}}]}`)
	if denied.Code != http.StatusForbidden || upstreamCalls.Load() != 0 {
		t.Fatalf("disallowed tool status=%d upstream_calls=%d", denied.Code, upstreamCalls.Load())
	}
	verifiedCaller, _, _, err := rt.verifyAccessToken(mission)
	if err != nil || verifiedCaller != caller {
		t.Fatalf("mission budget setup failed: %v", err)
	}
	var successful *httptest.ResponseRecorder
	for i := 0; i < 2; i++ {
		successful = post(`{"model":"native-stream","max_tokens":100,"messages":[{"role":"user","content":"x"}]}`)
		if successful.Code != http.StatusOK {
			t.Fatalf("mission request %d status=%d body=%s", i+1, successful.Code, successful.Body.String())
		}
	}
	spent := post(`{"model":"native-stream","max_tokens":100,"messages":[{"role":"user","content":"x"}]}`)
	if spent.Code != http.StatusForbidden {
		t.Fatalf("settled mission cap status=%d body=%s", spent.Code, spent.Body.String())
	}
	for i, want := range []int{9, 3} {
		select {
		case got := <-maxTokensForwarded:
			if got != want {
				t.Fatalf("request %d forwarded max_tokens=%d, want reduced limit %d", i+1, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("request %d did not reach upstream", i+1)
		}
	}
	var row usageRecord
	deadline := time.Now().Add(time.Second)
	for {
		err := svc.usage.db.First(&row, "request_id = ?", successful.Header().Get("X-Request-Id")).Error
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if row.TaskID != "mission-task-4" || row.HumanOwner != "owner-team" {
		t.Fatalf("request attribution missing: task=%q owner=%q", row.TaskID, row.HumanOwner)
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("successful mission request count=%d, want 2", upstreamCalls.Load())
	}
	svc.cfg.MCP.Enabled = true
	mcpRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	mcpRequest.Header.Set("Authorization", "Bearer "+mission)
	mcpResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(mcpResponse, mcpRequest)
	if mcpResponse.Code != http.StatusUnauthorized {
		t.Fatalf("mission token on unenforced MCP surface status=%d body=%s", mcpResponse.Code, mcpResponse.Body.String())
	}
	static := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","messages":[{"role":"user","content":"x"}]}`))
	static.Header.Set("Authorization", "Bearer "+testToken)
	staticResponse := httptest.NewRecorder()
	svc.Handler().ServeHTTP(staticResponse, static)
	if staticResponse.Code != http.StatusForbidden {
		t.Fatalf("forbid_long_lived_keys status=%d body=%s", staticResponse.Code, staticResponse.Body.String())
	}
}

func TestMissionTokenEmbeddingsGroupAndBudgetEnforcement(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 1)}
	svc := embeddingsTestService(t, stub, nil)
	caller, _ := svc.callerAuth.lookup(testToken)
	if caller == nil {
		t.Fatal("test caller unavailable")
	}
	issuer, subjectKey := runtimeJWTIssuerFixture(t, caller.cfg.ID, "federated")
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: caller.cfg.ID, HumanOwner: "owner-team", AllowedGroups: []string{"embed"}, MaxSpendUSD: .00000002, MaxTokens: 1}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: time.Minute, issuers: map[string]*tokenExchangeIssuer{issuer.cfg.Issuer: {auth: issuer, taskClaim: "task_id", grants: map[string]tokenExchangeGrant{"sub\x00automation": {config: grant}}}}, callers: map[string]*callerRuntime{caller.cfg.ID: caller}}
	svc.tokenExchange = rt
	subjectClaims := runtimeJWTClaims(issuer.cfg.Issuer)
	subjectClaims["task_id"] = "embedding-task"
	token, _, err := rt.exchange(context.Background(), machineSignedJWTType(t, subjectKey, "known", jose.ES256, "JWT", subjectClaims), []string{"embed"}, "embedding-task")
	if err != nil {
		t.Fatal(err)
	}
	post := func(model string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"`+model+`","input":"x"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	if got := post("small"); got.Code != http.StatusNotFound {
		t.Fatalf("out-of-scope embedding group status=%d body=%s", got.Code, got.Body.String())
	}
	if got := post("embed"); got.Code != http.StatusOK {
		t.Fatalf("in-scope embedding status=%d body=%s", got.Code, got.Body.String())
	}
	if got := post("embed"); got.Code != http.StatusForbidden {
		t.Fatalf("settled embedding token cap status=%d body=%s", got.Code, got.Body.String())
	}
	if calls := stub.calls.Load(); calls != 1 {
		t.Fatalf("embedding upstream calls=%d, want 1", calls)
	}
}

func TestMissionRequestMayReserveMoreThanHalfOfSpendBudget(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"completion","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer upstream.Close()
	var honorsMaxTokens = true
	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{{
		Provider: "native", Model: "up", ContextTokens: 4096, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 10, HonorsMaxTokens: &honorsMaxTokens,
		RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-chat"}, ValidationStatus: "passed", MaxEstimatedInputTokens: 100, MinRequestedOutputTokens: 1, MaxRequestedOutputTokens: 100},
		InputModalities:     []string{"text"}, OutputModalities: []string{"text"},
	}})
	caller, _ := svc.callerAuth.lookup(testToken)
	if caller == nil {
		t.Fatal("test caller unavailable")
	}
	issuer, subjectKey := runtimeJWTIssuerFixture(t, caller.cfg.ID, "federated")
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: caller.cfg.ID, HumanOwner: "owner-team", AllowedGroups: []string{"native-stream"}, MaxSpendUSD: .00005, MaxTokens: 4}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: time.Minute, issuers: map[string]*tokenExchangeIssuer{issuer.cfg.Issuer: {auth: issuer, taskClaim: "task_id", grants: map[string]tokenExchangeGrant{"sub\x00automation": {config: grant}}}}, callers: map[string]*callerRuntime{caller.cfg.ID: caller}}
	svc.tokenExchange = rt
	subjectClaims := runtimeJWTClaims(issuer.cfg.Issuer)
	subjectClaims["task_id"] = "one-request-task"
	token, _, err := rt.exchange(context.Background(), machineSignedJWTType(t, subjectKey, "known", jose.ES256, "JWT", subjectClaims), []string{"native-stream"}, "one-request-task")
	if err != nil {
		t.Fatal(err)
	}
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","max_tokens":100,"messages":[{"role":"user","content":"x"}]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, req)
		return w
	}
	first := post()
	if first.Code != http.StatusOK || upstreamCalls.Load() != 1 {
		t.Fatalf("request reserved > half of spend cap: status=%d calls=%d body=%s", first.Code, upstreamCalls.Load(), first.Body.String())
	}
	second := post()
	if second.Code != http.StatusForbidden || upstreamCalls.Load() != 1 {
		t.Fatalf("second request status=%d calls=%d body=%s, want budget rejection", second.Code, upstreamCalls.Load(), second.Body.String())
	}
}

func TestMissionConcurrentRequestsReserveAndSettleTogether(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"completion","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer upstream.Close()
	var honorsMaxTokens = true
	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{{
		Provider: "native", Model: "up", ContextTokens: 4096, InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 10, HonorsMaxTokens: &honorsMaxTokens,
		RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-chat"}, ValidationStatus: "passed", MaxEstimatedInputTokens: 100, MinRequestedOutputTokens: 1, MaxRequestedOutputTokens: 100},
		InputModalities:     []string{"text"}, OutputModalities: []string{"text"},
	}})
	caller, _ := svc.callerAuth.lookup(testToken)
	if caller == nil {
		t.Fatal("test caller unavailable")
	}
	issuer, subjectKey := runtimeJWTIssuerFixture(t, caller.cfg.ID, "federated")
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: caller.cfg.ID, HumanOwner: "owner-team", AllowedGroups: []string{"native-stream"}, MaxSpendUSD: .00008, MaxTokens: 8}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: time.Minute, issuers: map[string]*tokenExchangeIssuer{issuer.cfg.Issuer: {auth: issuer, taskClaim: "task_id", grants: map[string]tokenExchangeGrant{"sub\x00automation": {config: grant}}}}, callers: map[string]*callerRuntime{caller.cfg.ID: caller}}
	svc.tokenExchange = rt
	subjectClaims := runtimeJWTClaims(issuer.cfg.Issuer)
	subjectClaims["task_id"] = "concurrent-task"
	token, _, err := rt.exchange(context.Background(), machineSignedJWTType(t, subjectKey, "known", jose.ES256, "JWT", subjectClaims), []string{"native-stream"}, "concurrent-task")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"native-stream","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`))
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, req)
			results <- w
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case result := <-results:
			close(release)
			<-results
			t.Fatalf("concurrent request returned before both reached upstream: %d %s", result.Code, result.Body.String())
		case <-time.After(5 * time.Second):
			close(release)
			<-results
			<-results
			t.Fatal("both concurrent requests were not admitted to upstream")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.Code != http.StatusOK {
				t.Fatalf("concurrent mission request status=%d body=%s", result.Code, result.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent mission response timed out")
		}
	}
	_, _, claims, err := rt.verifyAccessToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if remaining, spend := rt.remainingMission(claims); remaining != 0 || spend != 0 {
		t.Fatalf("settled concurrent requests left budget tokens=%d spend=%f", remaining, spend)
	}
}

func configuredTokenExchangeHTTPFixture(t *testing.T) (*Service, *ecdsa.PrivateKey, string) {
	t.Helper()
	issuerKey := machineJWTKey(t)
	issuerURL := ""
	issuerServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuerURL, "jwks_uri": issuerURL + "/keys"})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &issuerKey.PublicKey, KeyID: "configured-kid", Algorithm: "ES256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuerServer.Close)
	issuerURL = issuerServer.URL
	host := strings.TrimPrefix(issuerURL, "https://")
	groups := []string{"small"}
	caller := &callerRuntime{cfg: CallerConfig{ID: "caller-a", Allow: groups}, allow: map[string]bool{"small": true}, keyStatus: accountStatusActive, ownerUserStatus: accountStatusActive, projectStatus: accountStatusActive, membershipStatus: accountStatusActive}
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: "caller-a", HumanOwner: "team-ml", AllowedGroups: groups, MaxSpendUSD: .25, MaxTokens: 100}
	tx := TokenExchangeConfig{Enabled: true, SigningKeyEnv: "MISSION_KEY", DefaultTTL: "1m", Issuers: []TokenExchangeIssuerConfig{{IssuerURL: issuerURL, Audience: "router-exchange", TaskClaim: "task_id", Algorithms: []string{"ES256"}, AllowedJWKSHosts: []string{host}, AllowPrivateNetwork: true, Subjects: []TokenExchangeSubjectGrant{
		grant,
		{Claim: "sub", Value: "system:serviceaccount:jobs:worker", CallerID: "caller-a", HumanOwner: "team-ml", TaskID: "k8s-worker-task", AllowedGroups: groups, MaxSpendUSD: .25, MaxTokens: 100},
		{Claim: "sub", Value: "spiffe://example.org/ns/default/sa/worker", CallerID: "caller-a", HumanOwner: "team-ml", TaskID: "spiffe-worker-task", AllowedGroups: groups, MaxSpendUSD: .25, MaxTokens: 100},
	}}}}
	cfg := &Config{Security: RouterSecurityConfig{TokenExchange: tx}, Callers: []CallerConfig{{ID: "caller-a", Allow: groups}}, Models: map[string]ModelGroup{"small": {Strategy: "static"}}}
	if err := validateTokenExchange(cfg); err != nil {
		t.Fatalf("validate exchange config: %v", err)
	}
	rt, err := newTokenExchangeRuntime(tx, map[string]*callerRuntime{"caller-a": caller}, func(name string) (string, bool) {
		if name == "MISSION_KEY" {
			return strings.Repeat("k", 32), true
		}
		return "", false
	}, issuerServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: cfg, mux: http.NewServeMux(), tokenExchange: rt}
	svc.mux.HandleFunc("POST /oauth/token", svc.handleTokenExchange)
	return svc, issuerKey, issuerURL
}

func TestTokenExchangeHTTPPositiveAndNegativePaths(t *testing.T) {
	svc, key, issuer := configuredTokenExchangeHTTPFixture(t)
	request := func(subject, group string, taskFields ...string) *httptest.ResponseRecorder {
		form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {subject}, "subject_token_type": {jwtSubjectTokenType}, "scope": {group}}
		if len(taskFields) > 0 && taskFields[0] != "" {
			form.Set("task_id", taskFields[0])
		}
		r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	claims := map[string]any{"iss": issuer, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "task_id": "task-99"}
	valid := machineSignedJWTType(t, key, "configured-kid", jose.ES256, "JWT", claims)
	w := request(valid, "small", "task-99")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("configured valid exchange status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, _, mission, err := svc.tokenExchange.verifyAccessToken(body.AccessToken); err != nil || mission.TaskID != "task-99" || body.ExpiresIn != 60 {
		t.Fatalf("issued token invalid: claims=%+v err=%v", mission, err)
	}
	if denied := request(valid, "small", "somebody-else"); denied.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned task_id assertion status=%d, want 401", denied.Code)
	}
	for _, tc := range []struct {
		name   string
		claims map[string]any
		key    *ecdsa.PrivateKey
		group  string
	}{
		{"wrong audience", map[string]any{"iss": issuer, "aud": "wrong", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "task_id": "task-99"}, key, "small"},
		{"wrong signing key", claims, machineJWTKey(t), "small"},
		{"ungranted subject", map[string]any{"iss": issuer, "aud": "router-exchange", "sub": "other", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "task_id": "task-99"}, key, "small"},
		{"missing signed task", map[string]any{"iss": issuer, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60}, key, "small"},
		{"invalid task charset", map[string]any{"iss": issuer, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "task_id": "task/99"}, key, "small"},
		{"outside grant scope", claims, key, "admin"},
		{"sender bound subject", map[string]any{"iss": issuer, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "task_id": "task-99", "cnf": map[string]any{"jkt": "thumbprint"}}, key, "small"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subject := machineSignedJWTType(t, tc.key, "configured-kid", jose.ES256, "JWT", tc.claims)
			denied := request(subject, tc.group)
			if denied.Code != http.StatusUnauthorized || strings.Contains(denied.Body.String(), subject) {
				t.Fatalf("configured negative exchange status=%d leaked-or-body=%s", denied.Code, denied.Body.String())
			}
		})
	}
	expiredClaims := map[string]any{"iss": issuer, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 120, "exp": time.Now().Unix() - 60, "task_id": "task-99"}
	if denied := request(machineSignedJWTType(t, key, "configured-kid", jose.ES256, "JWT", expiredClaims), "small"); denied.Code != http.StatusUnauthorized {
		t.Fatalf("expired workload token status=%d body=%s", denied.Code, denied.Body.String())
	}
	for _, tc := range []struct{ label, subject, typ string }{
		{"kubernetes-service-account", "system:serviceaccount:jobs:worker", "JWT"},
		{"spiffe-svid", "spiffe://example.org/ns/default/sa/worker", ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			shaped := map[string]any{"iss": issuer, "aud": "router-exchange", "sub": tc.subject, "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60}
			if tc.label == "kubernetes-service-account" {
				shaped["kubernetes.io"] = map[string]any{"namespace": "jobs", "serviceaccount": map[string]any{"name": "worker", "uid": "fixture-uid"}}
			}
			got := request(machineSignedJWTType(t, key, "configured-kid", jose.ES256, tc.typ, shaped), "small")
			if got.Code != http.StatusOK {
				t.Fatalf("realistic subject JWT status=%d body=%s", got.Code, got.Body.String())
			}
			var result struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			_, _, mission, err := svc.tokenExchange.verifyAccessToken(result.AccessToken)
			wantTask := "k8s-worker-task"
			if tc.label == "spiffe-svid" {
				wantTask = "spiffe-worker-task"
			}
			if err != nil || mission.TaskID != wantTask {
				t.Fatalf("realistic subject task=%q err=%v, want pinned %q", mission.TaskID, err, wantTask)
			}
		})
	}
}

func TestMissionTokenSurvivesExpiryDuringRealHandlerStream(t *testing.T) {
	var upstreamCalls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamCalls.Add(1) == 1 {
			close(started)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream response writer does not flush")
			return
		}
		fmt.Fprint(w, "data: {\"id\":\"mission-expiry\",\"model\":\"up\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"started\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"id\":\"mission-expiry\",\"model\":\"up\",\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()
	var honorsMaxTokens = true
	target := Target{
		Provider: "native", Model: "up", ContextTokens: 4096,
		InputPricePerMillionUSD: 10, OutputPricePerMillionUSD: 10, HonorsMaxTokens: &honorsMaxTokens,
		RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-chat"}, ValidationStatus: "passed", MaxEstimatedInputTokens: 100, MinRequestedOutputTokens: 1, MaxRequestedOutputTokens: 100},
		InputModalities:     []string{"text"}, OutputModalities: []string{"text"},
	}
	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{target})
	caller, _ := svc.callerAuth.lookup(testToken)
	if caller == nil {
		t.Fatal("test caller unavailable")
	}
	issuer, subjectKey := runtimeJWTIssuerFixture(t, caller.cfg.ID, "federated")
	grant := TokenExchangeSubjectGrant{Claim: "sub", Value: "automation", CallerID: caller.cfg.ID, HumanOwner: "owner-team", AllowedGroups: []string{"native-stream"}, MaxSpendUSD: .00011, MaxTokens: 100}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: 2 * time.Second, issuers: map[string]*tokenExchangeIssuer{issuer.cfg.Issuer: {auth: issuer, taskClaim: "task_id", grants: map[string]tokenExchangeGrant{"sub\x00automation": {config: grant}}}}, callers: map[string]*callerRuntime{caller.cfg.ID: caller}, budgets: make(map[string]*missionBudget)}
	svc.tokenExchange = rt
	subjectClaims := runtimeJWTClaims(issuer.cfg.Issuer)
	subjectClaims["task_id"] = "stream-expiry-task"
	subject := machineSignedJWTType(t, subjectKey, "known", jose.ES256, "JWT", subjectClaims)
	missionToken, _, err := rt.exchange(context.Background(), subject, []string{"native-stream"}, "stream-expiry-task")
	if err != nil {
		t.Fatal(err)
	}
	_, _, mission, err := rt.verifyAccessToken(missionToken)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	request := func(token string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-stream","stream":true,"stream_options":{"include_usage":true},"max_tokens":6,"messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first := request(missionToken)
	if first.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(first.Body)
		first.Body.Close()
		t.Fatalf("stream request status=%d body=%s", first.StatusCode, body)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		first.Body.Close()
		t.Fatal("stream did not reach upstream")
	}
	if wait := time.Until(mission.Expiry.Time().Add(100 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	if _, _, _, err := rt.verifyAccessToken(missionToken); err == nil {
		first.Body.Close()
		t.Fatal("mission token did not expire while the upstream stream was held")
	}
	unblock()
	streamBody, err := io.ReadAll(first.Body)
	first.Body.Close()
	if err != nil {
		t.Fatalf("read completed stream after token expiry: %v", err)
	}
	if !strings.Contains(string(streamBody), `"total_tokens":6`) || !strings.Contains(string(streamBody), "[DONE]") {
		t.Fatalf("stream did not deliver final usage and completion after expiry: %s", streamBody)
	}

	rt.budgetMu.Lock()
	budget := rt.budgets[mission.GrantID]
	if budget == nil {
		rt.budgetMu.Unlock()
		t.Fatal("stream finish did not create the grant budget")
	}
	usedTokens, usedSpend := budget.usedTokens, budget.usedSpend
	reservedTokens, reservedSpend := budget.reservedTokens, budget.reservedSpend
	rt.budgetMu.Unlock()
	if usedTokens != 6 || usedSpend <= 0 || usedSpend >= grant.MaxSpendUSD || reservedTokens != 0 || reservedSpend != 0 {
		t.Fatalf("late stream settlement tokens=%d spend=%g reserved=(%d,%g)", usedTokens, usedSpend, reservedTokens, reservedSpend)
	}

	refreshedToken, _, err := rt.exchange(context.Background(), subject, []string{"native-stream"}, "stream-expiry-task")
	if err != nil {
		t.Fatalf("re-exchange after streamed settlement failed: %v", err)
	}
	refreshed := request(refreshedToken)
	defer refreshed.Body.Close()
	if refreshed.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(refreshed.Body)
		t.Fatalf("re-exchanged token bypassed settled spend cap: status=%d body=%s", refreshed.StatusCode, body)
	}
	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("upstream calls=%d after rejected re-exchange request, want 1", got)
	}
}

func TestTokenExchangeHTTPAllowsNoTaskSource(t *testing.T) {
	svc, key, issuer := configuredTokenExchangeHTTPFixture(t)
	config := svc.cfg.Security.TokenExchange.Issuers[0]
	config.TaskClaim = ""
	config.Subjects = []TokenExchangeSubjectGrant{
		{Claim: "sub", Value: "system:serviceaccount:jobs:worker", CallerID: "caller-a", HumanOwner: "team-ml", AllowedGroups: []string{"small"}, MaxSpendUSD: .25, MaxTokens: 100},
		{Claim: "sub", Value: "spiffe://example.org/ns/default/sa/worker", CallerID: "caller-a", HumanOwner: "team-ml", AllowedGroups: []string{"small"}, MaxSpendUSD: .25, MaxTokens: 100},
	}
	svc.tokenExchange.installIssuerConfig(TokenExchangeConfig{Enabled: true, Issuers: []TokenExchangeIssuerConfig{config}})
	post := func(subject, task string) *httptest.ResponseRecorder {
		form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {subject}, "subject_token_type": {jwtSubjectTokenType}, "scope": {"small"}}
		if task != "" {
			form.Set("task_id", task)
		}
		r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct{ name, subject, typ string }{
		{"kubernetes-service-account", "system:serviceaccount:jobs:worker", "JWT"},
		{"spiffe-jwt-svid", "spiffe://example.org/ns/default/sa/worker", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"iss": issuer, "aud": []string{"router-exchange"}, "sub": tc.subject, "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60}
			if tc.name == "kubernetes-service-account" {
				claims["kubernetes.io"] = map[string]any{
					"namespace":      "jobs",
					"serviceaccount": map[string]any{"name": "worker", "uid": "fixture-sa-uid"},
					"pod":            map[string]any{"name": "worker-7d9f", "uid": "fixture-pod-uid"},
				}
			}
			subject := machineSignedJWTType(t, key, "configured-kid", jose.ES256, tc.typ, claims)
			issued := post(subject, "")
			if issued.Code != http.StatusOK {
				t.Fatalf("taskless realistic subject status=%d body=%s", issued.Code, issued.Body.String())
			}
			var result struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(issued.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			_, _, mission, err := svc.tokenExchange.verifyAccessToken(result.AccessToken)
			if err != nil || mission.TaskID != "" {
				t.Fatalf("taskless realistic mission claims=%+v err=%v", mission, err)
			}
			if denied := post(subject, "invented-task"); denied.Code != http.StatusUnauthorized {
				t.Fatalf("form task created attribution without a configured source: status=%d body=%s", denied.Code, denied.Body.String())
			}
		})
	}
}

func TestTokenExchangeHTTPUsesDottedTaskClaim(t *testing.T) {
	svc, key, issuer := configuredTokenExchangeHTTPFixture(t)
	config := svc.cfg.Security.TokenExchange
	config.Issuers[0].TaskClaim = "workflow.task.id"
	svc.tokenExchange.installIssuerConfig(config)
	claims := map[string]any{"iss": issuer, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "workflow": map[string]any{"task": map[string]any{"id": "nested-task-7"}}}
	subject := machineSignedJWTType(t, key, "configured-kid", jose.ES256, "JWT", claims)
	form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {subject}, "subject_token_type": {jwtSubjectTokenType}, "scope": {"small"}, "task_id": {"nested-task-7"}}
	r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("nested task claim exchange status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	_, _, mission, err := svc.tokenExchange.verifyAccessToken(result.AccessToken)
	if err != nil || mission.TaskID != "nested-task-7" {
		t.Fatalf("nested task claim=%q err=%v", mission.TaskID, err)
	}
}

func TestTokenExchangeConfigValidatesTaskClaimAndPinnedTask(t *testing.T) {
	svc, _, _ := configuredTokenExchangeHTTPFixture(t)
	invalidPath := *svc.cfg
	invalidPath.Security.TokenExchange.Issuers = append([]TokenExchangeIssuerConfig(nil), svc.cfg.Security.TokenExchange.Issuers...)
	invalidPath.Security.TokenExchange.Issuers[0].TaskClaim = "workflow..task"
	if err := validateTokenExchange(&invalidPath); err == nil {
		t.Fatal("invalid dotted task_claim path accepted")
	}
	invalidTask := *svc.cfg
	invalidTask.Security.TokenExchange.Issuers = append([]TokenExchangeIssuerConfig(nil), svc.cfg.Security.TokenExchange.Issuers...)
	invalidTask.Security.TokenExchange.Issuers[0].Subjects = append([]TokenExchangeSubjectGrant(nil), invalidTask.Security.TokenExchange.Issuers[0].Subjects...)
	invalidTask.Security.TokenExchange.Issuers[0].Subjects[0].TaskID = "bad/task"
	if err := validateTokenExchange(&invalidTask); err == nil {
		t.Fatal("invalid grant-pinned task_id accepted")
	}
}

func TestTokenExchangeIssuerReloadReusesUnchangedAuthenticator(t *testing.T) {
	svc, _, issuerURL := configuredTokenExchangeHTTPFixture(t)
	issuer := svc.tokenExchange.currentIssuer(issuerURL)
	if issuer == nil {
		t.Fatal("configured issuer missing")
	}
	auth, err := issuer.getAuthenticator(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	svc.tokenExchange.installIssuerConfig(svc.cfg.Security.TokenExchange)
	if got := svc.tokenExchange.currentIssuer(issuerURL); got != issuer || got.auth != auth {
		t.Fatal("unchanged issuer reload replaced its cached discovery/JWKS authenticator")
	}
	changed := svc.cfg.Security.TokenExchange
	changed.Issuers = append([]TokenExchangeIssuerConfig(nil), changed.Issuers...)
	changed.Issuers[0].Audience = "changed-audience"
	svc.tokenExchange.installIssuerConfig(changed)
	if got := svc.tokenExchange.currentIssuer(issuerURL); got == issuer || got.auth != nil {
		t.Fatal("changed issuer configuration retained stale discovery/JWKS state")
	}
}

func TestTokenExchangeIssuerReloadPreservesGrantIdentityAndBudget(t *testing.T) {
	svc, key, issuerURL := configuredTokenExchangeHTTPFixture(t)
	claims := map[string]any{"iss": issuerURL, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "task_id": "task-99"}
	subject := machineSignedJWTType(t, key, "configured-kid", jose.ES256, "JWT", claims)
	form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {subject}, "subject_token_type": {jwtSubjectTokenType}, "scope": {"small"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	svc.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("token exchange status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.AccessToken == "" {
		t.Fatalf("decode access token: err=%v payload=%+v", err, payload)
	}
	rt := svc.tokenExchange
	rt.quota = &quotaStore{callers: rt.callers, state: persistentState{Callers: map[string]*callerState{}}}
	grantID := missionGrantIdentity(issuerURL, "sub:automation")
	rt.budgets[grantID] = &missionBudget{usedTokens: 7, usedSpend: .02}
	rt.installIssuerConfig(svc.cfg.Security.TokenExchange)
	if _, _, verified, err := rt.verifyAccessToken(payload.AccessToken); err != nil || verified.GrantID != grantID {
		t.Fatalf("mission token after unchanged reload: grant=%q err=%v", verifiedGrantID(verified), err)
	}
	if rt.budgets[grantID] == nil || rt.budgets[grantID].usedTokens != 7 || rt.budgets[grantID].usedSpend != .02 {
		t.Fatalf("unchanged issuer reload lost cumulative budget: %+v", rt.budgets[grantID])
	}
}

func verifiedGrantID(claims *missionTokenClaims) string {
	if claims == nil {
		return ""
	}
	return claims.GrantID
}

func TestTokenExchangeWritesSecurityEvents(t *testing.T) {
	svc, key, issuer := configuredTokenExchangeHTTPFixture(t)
	path := filepath.Join(t.TempDir(), "security-events.jsonl")
	stream, err := newSecurityEventStream(SecurityEventStreamConfig{Output: "file", Path: path}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	svc.securityEventStream = stream

	claims := map[string]any{"iss": issuer, "aud": "router-exchange", "sub": "automation", "iat": time.Now().Unix() - 1, "exp": time.Now().Unix() + 60, "task_id": "task-99"}
	subject := machineSignedJWTType(t, key, "configured-kid", jose.ES256, "JWT", claims)
	post := func(raw string) int {
		form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {raw}, "subject_token_type": {jwtSubjectTokenType}, "scope": {"small"}}
		r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if status := post(subject); status != http.StatusOK {
		stream.close()
		t.Fatalf("valid exchange status=%d", status)
	}
	if status := post("do-not-write-this-subject-token"); status != http.StatusUnauthorized {
		stream.close()
		t.Fatalf("invalid exchange status=%d", status)
	}
	stream.close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"Authentication", "mission_token_issued", "caller-a", missionGrantIdentity(issuer, "sub:automation"), "api_auth_failed unauthorized"} {
		if !strings.Contains(text, want) {
			t.Fatalf("security event output missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, subject) || strings.Contains(text, "do-not-write-this-subject-token") {
		t.Fatal("security event output contains a subject token")
	}
}

func canceledTokenExchangeContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
