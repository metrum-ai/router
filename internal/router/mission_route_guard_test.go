// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type missionRouteCase struct {
	method string
	path   string
	allow  bool
}

func missionCallerRouteCases() []missionRouteCase {
	routes := []missionRouteCase{
		{http.MethodPost, "/mcp", false},
		{http.MethodGet, "/healthz", false},
		{http.MethodGet, "/readyz", false},
		{http.MethodGet, "/version", false},
		{http.MethodGet, "/.well-known/security.txt", false},
		{http.MethodGet, "/", false},
		{http.MethodPost, "/oauth/token", false},
		{http.MethodGet, "/v1/models", true},
		{http.MethodGet, "/v1/models/native-stream", true},
		{http.MethodGet, "/v1/codex/models.json", false},
		{http.MethodGet, "/v1/usage", false},
		{http.MethodPost, "/v1/security/leaked-keys", false},
		{http.MethodGet, "/metrics", false},
		{http.MethodDelete, "/v1/content-captures/request-1", false},
		{http.MethodPost, "/v1/content-captures/purge-expired", false},
		{http.MethodPost, "/v1/chat/completions", true},
		{http.MethodPost, "/v1/responses", true},
		{http.MethodPost, "/v1/messages", true},
		{http.MethodPost, "/anthropic/v1/messages", true},
		{http.MethodPost, "/v1/messages/count_tokens", true},
		{http.MethodPost, "/anthropic/v1/messages/count_tokens", true},
		{http.MethodPost, "/v1/responses/input_tokens", true},
		{http.MethodPost, "/v1/responses/compact", false},
		{http.MethodGet, "/v1/responses/resp-1", false},
		{http.MethodDelete, "/v1/responses/resp-1", false},
		{http.MethodGet, "/v1/responses/resp-1/input_items", false},
		{http.MethodPost, "/v1/responses/resp-1/cancel", false},
		{http.MethodPost, "/v1/embeddings", true},
		{http.MethodPost, "/v1/completions", false},
		{http.MethodPost, "/v1/moderations", false},
		{http.MethodPost, "/v1/content_provenance_checks", false},
		{http.MethodGet, "/admin/auth/login", false},
		{http.MethodGet, "/admin/auth/callback", false},
		{http.MethodPost, "/admin/auth/logout", false},
		{http.MethodGet, "/admin/auth/me", false},
		{http.MethodGet, "/admin/auth/check", false},
		{http.MethodGet, "/admin/reports", false},
		{http.MethodGet, "/admin/reports/", false},
	}
	for _, prefix := range []string{"/v1", "/anthropic/v1"} {
		routes = append(routes, missionRouteCase{http.MethodPost, prefix + "/complete", false})
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			routes = append(routes, missionRouteCase{method, prefix + "/messages/batches", false})
		}
		routes = append(routes,
			missionRouteCase{http.MethodGet, prefix + "/messages/batches/batch-1", false},
			missionRouteCase{http.MethodDelete, prefix + "/messages/batches/batch-1", false},
			missionRouteCase{http.MethodGet, prefix + "/messages/batches/batch-1/results", false},
			missionRouteCase{http.MethodPost, prefix + "/messages/batches/batch-1/cancel", false},
		)
		for _, path := range []string{"/skills", "/skills/"} {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				routes = append(routes, missionRouteCase{method, prefix + path, false})
			}
		}
	}
	for _, path := range []string{
		"/v1/files", "/v1/files/", "/anthropic/v1/files", "/anthropic/v1/files/", "/v1/uploads", "/v1/uploads/",
		"/v1/batches", "/v1/batches/", "/v1/images", "/v1/images/", "/v1/audio", "/v1/audio/",
		"/v1/vector_stores", "/v1/vector_stores/", "/v1/assistants", "/v1/assistants/", "/v1/threads", "/v1/threads/",
		"/v1/fine_tuning", "/v1/fine_tuning/", "/v1/realtime", "/v1/realtime/", "/v1/videos", "/v1/videos/",
		"/v1/containers", "/v1/containers/", "/v1/conversations", "/v1/conversations/", "/v1/agents", "/v1/agents/",
		"/v1/chatkit/threads", "/v1/chatkit/threads/",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			routes = append(routes, missionRouteCase{method, path, false})
		}
	}
	return routes
}

func signedMissionTokenForRouteTest(t *testing.T, rt *tokenExchangeRuntime, caller *callerRuntime) string {
	t.Helper()
	now := time.Now()
	claims := missionTokenClaims{
		Claims:   jwt.Claims{Issuer: missionTokenIssuer, Subject: caller.cfg.ID, Audience: jwt.Audience{missionTokenAudience}, Expiry: jwt.NewNumericDate(now.Add(time.Minute)), IssuedAt: jwt.NewNumericDate(now), ID: requestID()},
		CallerID: caller.cfg.ID, Groups: []string{"native-stream"}, MaxSpendUSD: 1, MaxTokens: 100, AllowedTools: []string{"search"}, TaskID: "route-check", HumanOwner: "route-test",
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: rt.key}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMissionTokenRouteAllowListCoversServingRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected upstream request", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	var honorsMaxTokens = true
	svc := nativeStreamTestService(t, upstream.URL, "openai-chat", []Target{{
		Provider: "native", Model: "up", ContextTokens: 4096, HonorsMaxTokens: &honorsMaxTokens,
		RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-chat"}, ValidationStatus: "passed", MaxEstimatedInputTokens: 100, MinRequestedOutputTokens: 1, MaxRequestedOutputTokens: 100},
		InputModalities:     []string{"text"}, OutputModalities: []string{"text"},
	}})
	caller, _ := svc.callerAuth.lookup(testToken)
	if caller == nil {
		t.Fatal("test caller unavailable")
	}
	rt := &tokenExchangeRuntime{key: []byte(strings.Repeat("k", 32)), ttl: time.Minute, callers: map[string]*callerRuntime{caller.cfg.ID: caller}}
	svc.tokenExchange = rt
	token := signedMissionTokenForRouteTest(t, rt, caller)

	for _, route := range missionCallerRouteCases() {
		t.Run(fmt.Sprintf("%s %s", route.method, route.path), func(t *testing.T) {
			if got := missionTokenRouteAllowed(route.method, route.path); got != route.allow {
				t.Fatalf("route allow-list=%v, expected %v", got, route.allow)
			}
			req := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if route.allow && rr.Code == http.StatusUnauthorized {
				t.Fatalf("allow-listed route rejected mission token: %s", rr.Body.String())
			}
			if !route.allow && rr.Code != http.StatusUnauthorized {
				t.Fatalf("non-allow-listed route status=%d, want 401; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
	// Ensure an arbitrary new route remains denied without needing a handler.
	if missionTokenRouteAllowed(http.MethodPost, "/v1/future/capability") {
		t.Fatal("unknown caller route was not denied by default")
	}
}
