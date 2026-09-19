// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metrum-ai/router/internal/buildinfo"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func init() {
	// Parallel go test on the self-hosted runner often exceeds the 500ms
	// production lookup budget. Keep the production default unchanged.
	defaultImageURLDNSTimeout = 5 * time.Second
}

func TestAnthropicIngressUnaryHappyPath(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/anthropic/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			var upstreamAuth string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamAuth = r.Header.Get("Authorization")
				if r.URL.Path != "/v1/chat/completions" {
					t.Fatalf("unexpected upstream path %s", r.URL.Path)
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"id": "up_1",
					"choices": []map[string]any{{
						"message":       map[string]any{"role": "assistant", "content": "hello through router"},
						"finish_reason": "stop",
					}},
					"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7},
				})
			}))
			defer upstream.Close()

			cfg := testConfig(t, upstream.URL, "secret-provider-key", t.TempDir())
			cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{
				Provider: "mock",
				Model:    "mock-model",
				RequestShapeSupport: RequestShapeSupport{
					SupportedInboundDialects: []string{"anthropic"},
					ValidationStatus:         "passed",
				},
			}}}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"default","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("User-Agent", "claude-code-test")
			rr := httptest.NewRecorder()

			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "secret-provider-key") {
				t.Fatal("response leaked provider key")
			}
			if upstreamAuth != "Bearer secret-provider-key" {
				t.Fatalf("upstream auth not injected, got %q", upstreamAuth)
			}
			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["type"] != "message" {
				t.Fatalf("not an anthropic message response: %#v", body)
			}
		})
	}
}

func TestDecisionFilterReasonsDoNotMarkExistingTextTranslationsDialectUnsupported(t *testing.T) {
	svc := &Service{}
	req := &IRRequest{
		Model:    "translated",
		Messages: []IRMessage{{Role: "user", Content: "hello"}},
		Raw:      map[string]any{},
	}
	target := Target{
		Model:            "chat-target",
		InputModalities:  []string{"text"},
		OutputModalities: []string{"text"},
		ResponsesToChat:  ResponsesToChatBridge{Enabled: true, Text: true},
	}
	estimate := requestTokenEstimateFromIR(req, "openai-responses", 64)
	reasons := svc.requestFilterReasons(target, req, "openai-responses", "openai-chat", estimate)
	for _, reason := range reasons {
		if reason == "dialect-unsupported" {
			t.Fatalf("reasons=%#v should not mark existing Responses-to-Chat text translation as unsupported", reasons)
		}
	}
}

func TestAuthRejectsUnknownTokenBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer bogus")
	rr := httptest.NewRecorder()

	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("upstream was called for unauthorized request")
	}
	if _, tokenID, err := svc.authenticate("", ""); err == nil || tokenID != "missing-token" {
		t.Fatalf("missing token id=%q err=%v", tokenID, err)
	}
	if _, tokenID, err := svc.authenticate("Bearer bogus-secret-token", ""); err == nil || tokenID != "invalid-token" {
		t.Fatalf("invalid token id=%q err=%v", tokenID, err)
	}
}

func TestUpstreamUserAgentDefaultsAndAllowsOverride(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		var gotUA string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUA = r.Header.Get("User-Agent")
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "up_ua",
				"choices": []map[string]any{{
					"message":       map[string]any{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		}))
		defer upstream.Close()

		cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
		svc, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		want := "metrum-ai-router/" + buildinfo.Version
		if gotUA != want {
			t.Fatalf("User-Agent=%q, want %q", gotUA, want)
		}
	})

	t.Run("provider_headers_override", func(t *testing.T) {
		var gotUA string
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUA = r.Header.Get("User-Agent")
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "up_ua_override",
				"choices": []map[string]any{{
					"message":       map[string]any{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		}))
		defer upstream.Close()

		cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
		mock := cfg.Provider["mock"]
		mock.Headers = map[string]string{"User-Agent": "custom-operator-ua"}
		cfg.Provider["mock"] = mock
		svc, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if gotUA != "custom-operator-ua" {
			t.Fatalf("User-Agent=%q, want custom-operator-ua", gotUA)
		}
	})
}

func TestAuthRejectsInactiveCallerKeyAfterTokenMatch(t *testing.T) {
	for _, tt := range []struct {
		status string
		code   string
	}{
		{status: "disabled", code: "key-disabled"},
		{status: "suspended", code: "key-suspended"},
		{status: "expired", code: "key-expired"},
		{status: "rotated", code: "key-rotated"},
	} {
		t.Run(tt.status, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("upstream must not be called for inactive key")
			}))
			defer upstream.Close()
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Users = []UserConfig{{ID: "alice"}}
			cfg.Projects = []ProjectConfig{{ID: "metrum-insights"}}
			cfg.ProjectMemberships = []ProjectMembershipConfig{{UserID: "alice", Project: "metrum-insights", Role: "developer"}}
			cfg.Callers[0].OwnerUser = "alice"
			cfg.Callers[0].Status = tt.status
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()

			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), tt.code) {
				t.Fatalf("status=%d body=%s, want %s", rr.Code, rr.Body.String(), tt.code)
			}
		})
	}
}

func TestAuthAcceptsXAPIKeyForAnthropicStyleClients(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_x_api_key",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "x-api-key ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "mock",
		Model:    "mock-model",
		RequestShapeSupport: RequestShapeSupport{
			SupportedInboundDialects: []string{"anthropic"},
			ValidationStatus:         "passed",
		},
	}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-API-Key", testToken)
	rr := httptest.NewRecorder()

	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "x-api-key ok") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}

func TestAdminBasicAuthCheckDisabledIsNotPublic(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("disabled admin auth sent challenge: %q", rr.Header().Get("WWW-Authenticate"))
	}
}

func TestAdminBasicAuthCheckChallengesAndAuthorizes(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		Realm:             "Unit Test Admin",
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "local/test",
			Permissions:     []string{"admin:auth:read"},
		}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, tt := range []struct {
		name       string
		username   string
		password   string
		setAuth    bool
		wantStatus int
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "bad username", username: "operator", password: "yell-yell-yum", setAuth: true, wantStatus: http.StatusUnauthorized},
		{name: "bad password", username: "admin", password: "wrong", setAuth: true, wantStatus: http.StatusUnauthorized},
		{name: "valid", username: "admin", password: "yell-yell-yum", setAuth: true, wantStatus: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
			if tt.setAuth {
				req.SetBasicAuth(tt.username, tt.password)
			}
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("missing no-store cache header: %#v", rr.Header())
			}
			if tt.wantStatus == http.StatusUnauthorized && !strings.Contains(rr.Header().Get("WWW-Authenticate"), `Basic realm="Unit Test Admin"`) {
				t.Fatalf("missing Basic challenge: %#v", rr.Header())
			}
			if strings.Contains(rr.Body.String(), "yell-yell-yum") || strings.Contains(rr.Body.String(), hash) {
				t.Fatalf("admin auth response exposed credential material: %s", rr.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				var body map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["subject"] != "basic:admin" || body["domain"] != "local/test" || body["source"] != "basic" {
					t.Fatalf("unexpected subject response: %#v", body)
				}
			}
		})
	}
}

func TestAdminBasicAuthCheckRequiresPermissionAndHTTPS(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled: true,
		Realm:   "Unit Test Admin",
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "local/test",
		}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	httpReq := httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
	httpReq.SetBasicAuth("admin", "yell-yell-yum")
	httpRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(httpRR, httpReq)
	if httpRR.Code != http.StatusUnauthorized {
		t.Fatalf("plain http status=%d body=%s", httpRR.Code, httpRR.Body.String())
	}

	httpsReq := httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
	httpsReq.Header.Set("X-Forwarded-Proto", "https")
	httpsReq.SetBasicAuth("admin", "yell-yell-yum")
	httpsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(httpsRR, httpsReq)
	if httpsRR.Code != http.StatusUnauthorized {
		t.Fatalf("untrusted forwarded proto status=%d body=%s", httpsRR.Code, httpsRR.Body.String())
	}

	cfg.Server.AdminAuth.Basic.TrustedProxyCIDRs = []string{"192.0.2.0/24"}
	svcTrusted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svcTrusted.Close()
	trustedReq := httptest.NewRequest(http.MethodGet, "/admin/auth/check", nil)
	trustedReq.Header.Set("X-Forwarded-Proto", "https")
	trustedReq.SetBasicAuth("admin", "yell-yell-yum")
	trustedRR := httptest.NewRecorder()
	svcTrusted.Handler().ServeHTTP(trustedRR, trustedReq)
	if trustedRR.Code != http.StatusForbidden {
		t.Fatalf("missing permission status=%d body=%s", trustedRR.Code, trustedRR.Body.String())
	}
	if !strings.Contains(trustedRR.Body.String(), "admin-forbidden") {
		t.Fatalf("missing admin-forbidden body: %s", trustedRR.Body.String())
	}
}

func TestAdminBasicAuthDoesNotChangeProxyBearerAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_admin_basic_proxy",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", mustBcryptHash(t, "yell-yell-yum"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		Realm:             "Unit Test Admin",
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "local/test",
			Permissions:     []string{"admin:auth:read"},
		}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminOIDCLoginCallbackMeAndLogout(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	defer issuer.Close()
	svc := newTestOIDCAdminService(t, issuer, []string{
		"p, user:alice@example.com, example/prod, admin:reports, read",
	})
	defer svc.Close()

	login := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	loginRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(loginRR, login)
	if loginRR.Code != http.StatusFound {
		t.Fatalf("login status=%d body=%s", loginRR.Code, loginRR.Body.String())
	}
	location := loginRR.Header().Get("Location")
	authURL, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse auth redirect: %v", err)
	}
	q := authURL.Query()
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("login redirect missing OIDC state/nonce/PKCE fields: %s", location)
	}
	issuer.nextNonce.Store(q.Get("nonce"))

	callback := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state="+url.QueryEscape(q.Get("state"))+"&code=ok", nil)
	callbackRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(callbackRR, callback)
	if callbackRR.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", callbackRR.Code, callbackRR.Body.String())
	}
	cookies := callbackRR.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("callback cookies=%d, want 1", len(cookies))
	}
	sessionCookie := cookies[0]
	if sessionCookie.Name != "test_admin_session" || !sessionCookie.HttpOnly || sessionCookie.Secure || sessionCookie.Path != "/admin" || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookie attributes: %#v", sessionCookie)
	}
	if strings.Contains(callbackRR.Body.String(), "id_token") || strings.Contains(callbackRR.Body.String(), "client-secret") {
		t.Fatalf("callback exposed token or secret material: %s", callbackRR.Body.String())
	}

	me := httptest.NewRequest(http.MethodGet, "/admin/auth/me", nil)
	me.AddCookie(sessionCookie)
	meRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(meRR, me)
	if meRR.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", meRR.Code, meRR.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(meRR.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["source"] != "oidc_session" || body["subject"] != "user:alice@example.com" || body["domain"] != "example/prod" || body["email"] != "alice@example.com" {
		t.Fatalf("unexpected me response: %#v", body)
	}
	if strings.Contains(meRR.Body.String(), "id_token") || strings.Contains(meRR.Body.String(), "client-secret") {
		t.Fatalf("me response exposed token or secret material: %s", meRR.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "/admin/auth/logout", nil)
	logout.AddCookie(sessionCookie)
	logoutRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(logoutRR, logout)
	if logoutRR.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logoutRR.Code, logoutRR.Body.String())
	}

	meAfterLogout := httptest.NewRequest(http.MethodGet, "/admin/auth/me", nil)
	meAfterLogout.AddCookie(sessionCookie)
	meAfterLogoutRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(meAfterLogoutRR, meAfterLogout)
	if meAfterLogoutRR.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout status=%d body=%s", meAfterLogoutRR.Code, meAfterLogoutRR.Body.String())
	}
}

func TestAdminOIDCCallbackRejectsBadStateAndDomain(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	defer issuer.Close()
	svc := newTestOIDCAdminService(t, issuer, nil)
	defer svc.Close()

	badState := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state=bad&code=ok", nil)
	badStateRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(badStateRR, badState)
	if badStateRR.Code != http.StatusUnauthorized {
		t.Fatalf("bad state status=%d body=%s", badStateRR.Code, badStateRR.Body.String())
	}

	login := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	loginRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(loginRR, login)
	authURL, err := url.Parse(loginRR.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	issuer.nextNonce.Store(authURL.Query().Get("nonce"))
	issuer.nextEmail.Store("mallory@other.example")
	badDomain := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state="+url.QueryEscape(authURL.Query().Get("state"))+"&code=ok", nil)
	badDomainRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(badDomainRR, badDomain)
	if badDomainRR.Code != http.StatusUnauthorized {
		t.Fatalf("bad domain status=%d body=%s", badDomainRR.Code, badDomainRR.Body.String())
	}
	if len(badDomainRR.Result().Cookies()) != 0 {
		t.Fatalf("bad domain callback set cookies: %#v", badDomainRR.Result().Cookies())
	}
}

func TestAdminOIDCStateStoreBoundsPendingLogins(t *testing.T) {
	now := time.Date(2026, 6, 25, 15, 0, 0, 0, time.UTC)
	store := &adminOIDCStateStore{states: map[string]adminOIDCLoginState{}}
	for i := 0; i < adminOIDCMaxPendingLogin; i++ {
		ok := store.put(adminOIDCLoginState{
			state:     fmt.Sprintf("state-%d", i),
			nonce:     "nonce",
			clientKey: fmt.Sprintf("client-%d", i),
			expiresAt: now.Add(adminOIDCLoginTTL),
		}, now)
		if !ok {
			t.Fatalf("state %d was rejected before cap", i)
		}
	}
	if ok := store.put(adminOIDCLoginState{state: "overflow", nonce: "nonce", expiresAt: now.Add(adminOIDCLoginTTL)}, now); ok {
		t.Fatal("overflow state was accepted")
	}
	if ok := store.put(adminOIDCLoginState{state: "after-expiry", nonce: "nonce", expiresAt: now.Add(2 * adminOIDCLoginTTL)}, now.Add(adminOIDCLoginTTL+time.Second)); !ok {
		t.Fatal("state after pruning expired entries was rejected")
	}
}

func TestAdminOIDCStateStoreLimitsOneClientWithoutBlockingOthers(t *testing.T) {
	now := time.Date(2026, 6, 25, 15, 0, 0, 0, time.UTC)
	store := &adminOIDCStateStore{states: map[string]adminOIDCLoginState{}}
	for i := 0; i < adminOIDCMaxPendingLoginPerClient; i++ {
		if ok := store.put(adminOIDCLoginState{
			state:     fmt.Sprintf("same-client-%d", i),
			nonce:     "nonce",
			clientKey: "198.51.100.10",
			expiresAt: now.Add(adminOIDCLoginTTL),
		}, now); !ok {
			t.Fatalf("same-client state %d was rejected before per-client cap", i)
		}
	}
	if ok := store.put(adminOIDCLoginState{state: "same-client-overflow", nonce: "nonce", clientKey: "198.51.100.10", expiresAt: now.Add(adminOIDCLoginTTL)}, now); ok {
		t.Fatal("same-client overflow state was accepted")
	}
	if ok := store.put(adminOIDCLoginState{state: "other-client", nonce: "nonce", clientKey: "198.51.100.11", expiresAt: now.Add(adminOIDCLoginTTL)}, now); !ok {
		t.Fatal("different client was blocked by same-client pending states")
	}
	if ok := store.put(adminOIDCLoginState{state: "same-client-after-expiry", nonce: "nonce", clientKey: "198.51.100.10", expiresAt: now.Add(2 * adminOIDCLoginTTL)}, now.Add(adminOIDCLoginTTL+time.Second)); !ok {
		t.Fatal("same client was not allowed after pending states expired")
	}
}

func TestAdminOIDCLoginRateLimitIsPerClient(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	defer issuer.Close()
	svc := newTestOIDCAdminService(t, issuer, nil)
	defer svc.Close()

	for i := 0; i < adminOIDCMaxPendingLoginPerClient; i++ {
		req := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
		req.RemoteAddr = "198.51.100.10:1234"
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusFound {
			t.Fatalf("login %d status=%d body=%s", i, rr.Code, rr.Body.String())
		}
	}
	limited := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	limited.RemoteAddr = "198.51.100.10:1234"
	limitedRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(limitedRR, limited)
	if limitedRR.Code != http.StatusTooManyRequests || !strings.Contains(limitedRR.Body.String(), "oidc-login-rate-limited") {
		t.Fatalf("same-client overflow status=%d body=%s", limitedRR.Code, limitedRR.Body.String())
	}
	other := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	other.RemoteAddr = "198.51.100.11:1234"
	otherRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(otherRR, other)
	if otherRR.Code != http.StatusFound {
		t.Fatalf("different client status=%d body=%s", otherRR.Code, otherRR.Body.String())
	}
}

func TestAdminOIDCLoginRateLimitUsesConfiguredTrustedProxyClientIP(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	defer issuer.Close()
	svc := newTestOIDCAdminService(t, issuer, nil)
	defer svc.Close()
	storeIP := false
	svc.cfg.Server.ClientIP = ClientIPConfig{
		TrustedProxyCIDRs: []string{"192.0.2.0/24"},
		HeaderOrder:       []string{"X-Forwarded-For"},
		StoreIP:           &storeIP,
	}

	for i := 0; i < adminOIDCMaxPendingLoginPerClient; i++ {
		req := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		req.Header.Set("X-Forwarded-For", "198.51.100.10")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusFound {
			t.Fatalf("proxied login %d status=%d body=%s", i, rr.Code, rr.Body.String())
		}
	}
	limited := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	limited.RemoteAddr = "192.0.2.10:1234"
	limited.Header.Set("X-Forwarded-For", "198.51.100.10")
	limitedRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(limitedRR, limited)
	if limitedRR.Code != http.StatusTooManyRequests {
		t.Fatalf("same forwarded client status=%d body=%s", limitedRR.Code, limitedRR.Body.String())
	}
	other := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	other.RemoteAddr = "192.0.2.10:1234"
	other.Header.Set("X-Forwarded-For", "198.51.100.11")
	otherRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(otherRR, other)
	if otherRR.Code != http.StatusFound {
		t.Fatalf("different forwarded client status=%d body=%s", otherRR.Code, otherRR.Body.String())
	}
}

func TestAdminOIDCSessionAuthorizesReportsWithCasbin(t *testing.T) {
	issuer := newFakeOIDCIssuer(t)
	defer issuer.Close()
	svc := newTestOIDCAdminService(t, issuer, []string{
		"p, user:alice@example.com, example/prod, admin:reports, read",
	})
	defer svc.Close()
	cookie := loginTestOIDCAdmin(t, svc, issuer)

	report := httptest.NewRequest(http.MethodGet, "/admin/reports/", nil)
	report.AddCookie(cookie)
	reportRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(reportRR, report)
	if reportRR.Code != http.StatusOK {
		t.Fatalf("authorized report status=%d body=%s", reportRR.Code, reportRR.Body.String())
	}

	noPolicyIssuer := newFakeOIDCIssuer(t)
	defer noPolicyIssuer.Close()
	noPolicySvc := newTestOIDCAdminService(t, noPolicyIssuer, []string{
		"p, user:bob@example.com, example/prod, admin:reports, read",
	})
	defer noPolicySvc.Close()
	noPolicyCookie := loginTestOIDCAdmin(t, noPolicySvc, noPolicyIssuer)
	forbidden := httptest.NewRequest(http.MethodGet, "/admin/reports/", nil)
	forbidden.AddCookie(noPolicyCookie)
	forbiddenRR := httptest.NewRecorder()
	noPolicySvc.Handler().ServeHTTP(forbiddenRR, forbidden)
	if forbiddenRR.Code != http.StatusForbidden || !strings.Contains(forbiddenRR.Body.String(), "reports-forbidden") {
		t.Fatalf("forbidden report status=%d body=%s", forbiddenRR.Code, forbiddenRR.Body.String())
	}
}

func TestAdminEvidenceCompletenessDecisionTelemetryDisabledNotApplicable(t *testing.T) {
	sections, status, score := adminEvidenceCompletenessFor(
		usageRow{TargetProvider: "mock", Attempts: 1, Status: 200},
		false,
		false,
		[]requestAttemptRecord{{RequestID: "req", AttemptIndex: 1, StatusCode: 200}}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	if status == "partial_missing" || score != 100 {
		t.Fatalf("status=%s score=%d sections=%#v", status, score, sections)
	}
	for _, section := range sections {
		if section.Name == "target_eligibility" {
			if section.Status != "not_applicable" || section.Expected {
				t.Fatalf("target eligibility section=%#v, want not_applicable when decision telemetry disabled", section)
			}
			return
		}
	}
	t.Fatalf("missing target_eligibility section: %#v", sections)
}

func TestAdminReportsRequireBasicAndCasbinAuthorization(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_admin_reports",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 5, "total_tokens": 13},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:                 "mock",
		Model:                    "mock-model",
		ForceStoreFalse:          true,
		OutputTokenField:         "max_completion_tokens",
		HonorsMaxTokens:          boolPtr(true),
		InputModalities:          []string{"text"},
		OutputModalities:         []string{"text"},
		ContextTokens:            8192,
		ToolSupport:              ToolSupport{OpenAIChat: []string{"tools"}},
		InputPricePerMillionUSD:  0.25,
		OutputPricePerMillionUSD: 1.25,
	}}}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		Realm:             "Unit Test Admin",
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "metrum-insights/test",
		}, {
			Username:        "reader",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:reader",
			Domain:          "metrum-insights/test",
		}, {
			Username:        "global",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:global",
			Domain:          "metrum-insights/test",
		}},
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Policy: []string{
			"g, basic:admin, reports_admin, metrum-insights/test",
			"p, reports_admin, metrum-insights/test, admin:reports, read|export|drilldown",
			"p, basic:reader, metrum-insights/test, admin:reports, read",
			"p, basic:global, *, admin:reports, read|export|drilldown",
		},
	}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 1, ExportMarkdown: true}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for i := 0; i < 2; i++ {
		modelReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi `+strconv.Itoa(i)+`"}]}`))
		modelReq.Header.Set("Authorization", "Bearer "+testToken)
		modelRR := httptest.NewRecorder()
		svc.Handler().ServeHTTP(modelRR, modelReq)
		if modelRR.Code != http.StatusOK {
			t.Fatalf("model status=%d body=%s", modelRR.Code, modelRR.Body.String())
		}
	}
	errType := "upstream-timeout"
	ttfbMS := int64(125)
	upstreamMS := int64(30100)
	downstreamMS := int64(250)
	upstreamTPS := 42.5
	downstreamTPS := 39.25
	svc.usage.Emit(logRecord{
		TS:                                 time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		RequestID:                          "admin-report-synthetic-expensive",
		CallerID:                           "alice",
		CallerUser:                         "alice",
		CallerProject:                      "metrum-insights",
		CallerEnvironment:                  "test",
		CallerIP:                           "203.0.113.10",
		TokenID:                            "rtr_alice_test",
		Client:                             "codex-cli",
		InboundDialect:                     "openai-responses",
		RequestedModel:                     "default",
		ResolvedGroup:                      "default",
		Strategy:                           "weighted",
		TargetProvider:                     "mock",
		TargetModel:                        "mock-model",
		TargetDialect:                      "openai",
		Stream:                             true,
		Cache:                              "hit",
		Status:                             504,
		Attempts:                           2,
		FallbackUsed:                       true,
		LatencyMS:                          30150,
		TTFBMS:                             &ttfbMS,
		UpstreamMS:                         &upstreamMS,
		DownstreamMS:                       &downstreamMS,
		UpstreamOutputTPS:                  &upstreamTPS,
		DownstreamOutputTPS:                &downstreamTPS,
		Usage:                              Usage{InputTokens: 1_000_000, OutputTokens: 500_000, TotalTokens: 1_500_000},
		InputHasImage:                      true,
		InputImageCount:                    1,
		InputImageTokens:                   1234,
		PIIFilterApplied:                   true,
		PIIFilterMode:                      "redact",
		PIIFilterReplacements:              2,
		PIIFilterRuleCount:                 1,
		InputPricePerMillionUSD:            1.25,
		OutputPricePerMillionUSD:           2.50,
		ImageInputPricePerMillionTokensUSD: 0.75,
		InputCostUSD:                       1.25,
		ImageCostUSD:                       0.25,
		OutputCostUSD:                      1.25,
		TotalCostUSD:                       2.75,
		UpstreamReportedInputCostUSD:       1.30,
		UpstreamReportedOutputCostUSD:      1.45,
		UpstreamReportedTotalCostUSD:       2.95,
		PricingSource:                      "https://example.test/pricing",
		PricingUpdatedAt:                   "2026-06-29",
		CacheEnabled:                       true,
		CacheItems:                         7,
		CacheBytes:                         4096,
		CacheMaxBytes:                      8192,
		CacheOccupancyPct:                  50,
		QuotaState:                         "soft_limit",
		KeyState:                           "ok",
		Error:                              &errType,
		ErrorClass:                         "timeout",
		ErrorMessage:                       "upstream timeout",
		AttemptsDetail: []attemptLogRecord{{
			Index:        1,
			TS:           time.Now().UTC().Format(time.RFC3339),
			Provider:     "mock",
			Model:        "mock-model",
			Dialect:      "openai",
			DurationMS:   30100,
			StatusCode:   504,
			ErrorClass:   "timeout",
			ErrorMessage: "upstream timeout",
			Retryable:    true,
			TimedOut:     true,
			Selected:     true,
			RetryAfterMS: 1500,
		}},
		DecisionShapeFeatures: []decisionShapeFeatureLogRecord{{Seq: 1, Name: "has_tools", BoolValue: true}},
		DecisionCandidates: []decisionCandidateLogRecord{{
			CandidateIndex:   0,
			GroupTargetIndex: 0,
			Provider:         "mock",
			Model:            "mock-model",
			Dialect:          "openai",
			ContextTokens:    8192,
			ToolSupport:      true,
			StructuredOutput: true,
			HonorsMaxTokens:  true,
			Eligible:         true,
			Selected:         true,
		}},
		DecisionFilterReasons: []decisionFilterReasonLogRecord{{Seq: 1, CandidateIndex: 1, Stage: "request_shape", Reason: "tool-support"}},
		RoutingDecisions:      []routingDecisionLogRecord{{Seq: 1, Strategy: "dynamic_score", SelectedCandidateIndex: 0, Provider: "mock", Model: "mock-model", Dialect: "openai", FallbackCount: 1}},
		RoutingSignals:        []routingSignalLogRecord{{Seq: 1, Strategy: "dynamic_score", SignalName: "observed_performance", Source: "dynamic_score", BoolValue: true}},
		DynamicScoreTerms:     []dynamicScoreTermLogRecord{{Seq: 1, CandidateIndex: 0, Rank: 1, Provider: "mock", Model: "mock-model", Dialect: "openai", TermName: "default", ScoreName: "latency_score", Weight: 0.5, Value: 0.8, Contribution: 0.4, FinalScore: 0.7, ObservationCount: 3, Selected: true}},
		PolicyExecutions:      []policyExecutionLogRecord{{Seq: 1, Strategy: "script", PolicyKind: "typescript", Outcome: "selected", DurationMS: 12, EligibleTargetCount: 1, SelectedCandidateIndex: 0}},
		CacheReasons:          []cacheReasonLogRecord{{Seq: 1, Status: "bypass", Reason: "cache-tool-request", CandidateIndex: 0, Provider: "mock", Model: "mock-model", Dialect: "openai"}},
	})
	svc.usage.Emit(logRecord{
		TS:                time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		RequestID:         "admin-report-cross-domain",
		CallerID:          "mallory",
		CallerUser:        "mallory",
		CallerProject:     "other-project",
		CallerEnvironment: "prod",
		TokenID:           "rtr_other_project_prod",
		Client:            "codex-cli",
		InboundDialect:    "openai-responses",
		RequestedModel:    "default",
		ResolvedGroup:     "default",
		Strategy:          "weighted",
		TargetProvider:    "mock",
		TargetModel:       "mock-model",
		TargetDialect:     "openai",
		Cache:             "miss",
		Status:            200,
		Attempts:          1,
		Usage:             Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		QuotaState:        "ok",
		KeyState:          "ok",
	})

	unauth := httptest.NewRequest(http.MethodGet, "/admin/reports/api/summary?since=24h", nil)
	unauthRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(unauthRR, unauth)
	if unauthRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d body=%s", unauthRR.Code, unauthRR.Body.String())
	}

	ordinary := httptest.NewRequest(http.MethodGet, "/admin/reports/api/summary?since=24h", nil)
	ordinary.Header.Set("Authorization", "Bearer "+testToken)
	ordinaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ordinaryRR, ordinary)
	if ordinaryRR.Code != http.StatusForbidden || !strings.Contains(ordinaryRR.Body.String(), "reports-forbidden") {
		t.Fatalf("ordinary status=%d body=%s", ordinaryRR.Code, ordinaryRR.Body.String())
	}
	ordinaryEvidence := httptest.NewRequest(http.MethodGet, "/admin/reports/api/request-evidence?request_id=admin-report-synthetic-expensive", nil)
	ordinaryEvidence.Header.Set("Authorization", "Bearer "+testToken)
	ordinaryEvidenceRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ordinaryEvidenceRR, ordinaryEvidence)
	if ordinaryEvidenceRR.Code != http.StatusForbidden || !strings.Contains(ordinaryEvidenceRR.Body.String(), "reports-forbidden") {
		t.Fatalf("ordinary evidence status=%d body=%s", ordinaryEvidenceRR.Code, ordinaryEvidenceRR.Body.String())
	}

	versionUnauth := httptest.NewRequest(http.MethodGet, "/admin/reports/api/version", nil)
	versionUnauthRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(versionUnauthRR, versionUnauth)
	if versionUnauthRR.Code != http.StatusUnauthorized {
		t.Fatalf("version unauth status=%d body=%s", versionUnauthRR.Code, versionUnauthRR.Body.String())
	}
	versionOrdinary := httptest.NewRequest(http.MethodGet, "/admin/reports/api/version", nil)
	versionOrdinary.Header.Set("Authorization", "Bearer "+testToken)
	versionOrdinaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(versionOrdinaryRR, versionOrdinary)
	if versionOrdinaryRR.Code != http.StatusForbidden || !strings.Contains(versionOrdinaryRR.Body.String(), "reports-forbidden") {
		t.Fatalf("version ordinary status=%d body=%s", versionOrdinaryRR.Code, versionOrdinaryRR.Body.String())
	}
	versionReq := httptest.NewRequest(http.MethodGet, "/admin/reports/api/version", nil)
	versionReq.SetBasicAuth("admin", "yell-yell-yum")
	versionRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(versionRR, versionReq)
	if versionRR.Code != http.StatusOK {
		t.Fatalf("version status=%d body=%s", versionRR.Code, versionRR.Body.String())
	}
	versionBody := mustJSONMap(t, versionRR.Body.String())
	for _, key := range []string{"version", "commit", "build_date", "go_version", "goos", "goarch", "license_compile_mode"} {
		if versionBody[key] == "" {
			t.Fatalf("version response missing %s: %#v", key, versionBody)
		}
	}
	for _, forbidden := range []string{"token_sha256", "provider-key", testToken, "messages"} {
		if strings.Contains(versionRR.Body.String(), forbidden) {
			t.Fatalf("version leaked %q: %s", forbidden, versionRR.Body.String())
		}
	}

	summary := httptest.NewRequest(http.MethodGet, "/admin/reports/api/summary?since=24h", nil)
	summary.SetBasicAuth("admin", "yell-yell-yum")
	summaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(summaryRR, summary)
	if summaryRR.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", summaryRR.Code, summaryRR.Body.String())
	}
	if summaryRR.Header().Get("Cache-Control") != "no-store" || !strings.Contains(summaryRR.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("missing admin report security headers: %#v", summaryRR.Header())
	}
	var body map[string]any
	if err := json.Unmarshal(summaryRR.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["summary"].(map[string]any)["requests"].(float64) < 2 {
		t.Fatalf("summary did not include request: %#v", body)
	}
	if strings.Contains(summaryRR.Body.String(), "admin-report-cross-domain") || strings.Contains(summaryRR.Body.String(), "other-project") {
		t.Fatalf("domain-scoped summary leaked other domain row: %s", summaryRR.Body.String())
	}
	charts := body["charts"].([]any)
	if len(charts) == 0 {
		t.Fatalf("summary missing chart contract: %#v", body)
	}
	firstChart := charts[0].(map[string]any)
	for _, key := range []string{"chart_id", "title", "x_axis", "y_axis", "series", "generated_at", "from", "to", "filters"} {
		if _, ok := firstChart[key]; !ok {
			t.Fatalf("chart missing %s: %#v", key, firstChart)
		}
	}
	xAxis := firstChart["x_axis"].(map[string]any)
	yAxis := firstChart["y_axis"].(map[string]any)
	if xAxis["label"] == "" || xAxis["type"] == "" || yAxis["label"] == "" || yAxis["unit"] == "" {
		t.Fatalf("chart axes missing labels/units: %#v %#v", xAxis, yAxis)
	}
	chartSeries := firstChart["series"].([]any)
	if len(chartSeries) == 0 {
		t.Fatalf("chart missing series: %#v", firstChart)
	}
	series0 := chartSeries[0].(map[string]any)
	for _, key := range []string{"name", "unit", "color_key", "points"} {
		if _, ok := series0[key]; !ok {
			t.Fatalf("chart series missing %s: %#v", key, series0)
		}
	}
	points := series0["points"].([]any)
	if len(points) == 0 {
		t.Fatalf("chart series missing scalar points: %#v", series0)
	}
	point0 := points[0].(map[string]any)
	if point0["x"] == "" {
		t.Fatalf("chart point missing x: %#v", point0)
	}
	if _, ok := point0["y"].(float64); !ok {
		t.Fatalf("chart point y is not numeric: %#v", point0)
	}
	for _, forbidden := range []string{"token_sha256", "provider-key", testToken, "messages"} {
		if strings.Contains(summaryRR.Body.String(), forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, summaryRR.Body.String())
		}
	}

	savings := httptest.NewRequest(http.MethodGet, "/admin/reports/api/savings?since=24h&baseline=gpt-5.5", nil)
	savings.SetBasicAuth("admin", "yell-yell-yum")
	savingsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(savingsRR, savings)
	if savingsRR.Code != http.StatusOK {
		t.Fatalf("savings status=%d body=%s", savingsRR.Code, savingsRR.Body.String())
	}
	var savingsBody map[string]any
	if err := json.Unmarshal(savingsRR.Body.Bytes(), &savingsBody); err != nil {
		t.Fatal(err)
	}
	baseline := savingsBody["baseline"].(map[string]any)
	if baseline["baseline_id"] != "gpt-5.5" || baseline["pricing_source"] == "" || baseline["pricing_updated_at"] == "" {
		t.Fatalf("savings baseline missing source metadata: %#v", baseline)
	}
	savingsSummary := savingsBody["summary"].(map[string]any)
	if savingsSummary["baseline_cost_usd"].(float64) <= 0 {
		t.Fatalf("savings baseline cost was not calculated from stored tokens: %#v", savingsSummary)
	}
	if len(savingsBody["charts"].([]any)) == 0 || len(savingsBody["byGroup"].([]any)) == 0 {
		t.Fatalf("savings missing charts or group rows: %#v", savingsBody)
	}
	for _, forbidden := range []string{"token_sha256", "provider-key", testToken, "messages"} {
		if strings.Contains(savingsRR.Body.String(), forbidden) {
			t.Fatalf("savings leaked %q: %s", forbidden, savingsRR.Body.String())
		}
	}

	reportPaths := []string{
		"/admin/reports/api/overview?since=24h",
		"/admin/reports/api/savings-by-user?since=24h&baseline=custom&baseline_input_price_per_million_usd=4&baseline_output_price_per_million_usd=8",
		"/admin/reports/api/savings-by-key?since=24h&baseline=custom&baseline_input_price_per_million_usd=4&baseline_output_price_per_million_usd=8",
		"/admin/reports/api/savings-by-key?since=24h&baseline=gpt-5.5&sort=savingsUsd&direction=desc",
		"/admin/reports/api/savings-by-group?since=24h&baseline=custom&baseline_input_price_per_million_usd=4&baseline_output_price_per_million_usd=8",
		"/admin/reports/api/savings-by-project?since=24h&baseline=custom&baseline_input_price_per_million_usd=4&baseline_output_price_per_million_usd=8",
		"/admin/reports/api/savings-by-provider-model?since=24h&baseline=custom&baseline_input_price_per_million_usd=4&baseline_output_price_per_million_usd=8",
		"/admin/reports/api/model-groups-by-user?since=24h",
		"/admin/reports/api/usage-by-key?since=24h",
		"/admin/reports/api/usage-by-caller?since=24h",
		"/admin/reports/api/requested-models?since=24h",
		"/admin/reports/api/provider-model-mix?since=24h",
		"/admin/reports/api/latency-throughput?since=24h",
		"/admin/reports/api/errors-fallbacks?since=24h",
		"/admin/reports/api/upstream-failures?since=24h",
		"/admin/reports/api/request-shape-failures?since=24h",
		"/admin/reports/api/fallback-health?since=24h",
		"/admin/reports/api/user-client-impact?since=24h",
		"/admin/reports/api/cache?since=24h",
		"/admin/reports/api/quotas-budgets?since=24h",
		"/admin/reports/api/traffic-shaping-overview?since=24h",
		"/admin/reports/api/traffic-shaping-by-user?since=24h",
		"/admin/reports/api/traffic-shaping-by-key?since=24h",
		"/admin/reports/api/traffic-shaping-by-client?since=24h",
		"/admin/reports/api/traffic-shaping-by-group?since=24h",
		"/admin/reports/api/provider-capacity-shaping?since=24h",
		"/admin/reports/api/adaptive-upstream-backoff?since=24h",
		"/admin/reports/api/traffic-tuning-advisor?since=24h",
		"/admin/reports/api/troubleshooting-buckets?since=24h",
		"/admin/reports/api/routing-decisions?since=24h",
		"/admin/reports/api/contract-buckets?since=24h",
		"/admin/reports/api/contract-workloads?since=24h",
		"/admin/reports/api/target-validation?since=24h",
		"/admin/reports/api/expensive-requests?since=24h&limit=1",
		"/admin/reports/api/client-breakdown?since=24h",
		"/admin/reports/api/project-chargeback?since=24h",
		"/admin/reports/api/capability-usage?since=24h&limit=1",
		"/admin/reports/api/anomalies?since=24h",
	}
	for _, path := range reportPaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth("admin", "yell-yell-yum")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
		body := mustJSONMap(t, rr.Body.String())
		if body["period"] == nil || body["summary"] == nil || body["generatedUtc"] == "" {
			t.Fatalf("%s missing common report shape: %#v", path, body)
		}
		if strings.Contains(path, "expensive-requests") {
			requestRows := body["requests"].([]any)
			if len(requestRows) != 1 || requestRows[0].(map[string]any)["requestId"] != "admin-report-synthetic-expensive" {
				t.Fatalf("%s did not return bounded cost-sorted requests: %#v", path, body)
			}
		} else if !strings.Contains(path, "overview") && !strings.Contains(path, "traffic-shaping") && !strings.Contains(path, "provider-capacity-shaping") && !strings.Contains(path, "adaptive-upstream-backoff") {
			rows := body["rows"].([]any)
			if len(rows) == 0 || len(rows) > 1 {
				t.Fatalf("%s rows len=%d, want bounded nonempty rows: %#v", path, len(rows), body)
			}
			row := rows[0].(map[string]any)
			if row["key"] == "" || row["requests"].(float64) <= 0 {
				t.Fatalf("%s missing scalar row key/request count: %#v", path, row)
			}
			if strings.Contains(path, "savings-by") && row["savingsUsd"] == nil {
				t.Fatalf("%s missing savings scalar fields: %#v", path, row)
			}
			if strings.Contains(path, "savings-by") {
				summary := body["summary"].(map[string]any)
				for _, key := range []string{"actualCostUsd", "baselineCostUsd", "savingsUsd", "savingsPct"} {
					if _, ok := summary[key]; !ok {
						t.Fatalf("%s missing savings summary field %q: %#v", path, key, summary)
					}
					if _, ok := row[key]; !ok {
						t.Fatalf("%s missing savings row field %q: %#v", path, key, row)
					}
				}
				if row["costUsd"] != row["totalCostUsd"] || row["actualCostUsd"] != row["totalCostUsd"] {
					t.Fatalf("%s actual cost aliases disagree: %#v", path, row)
				}
			}
			if strings.Contains(path, "provider-model-mix") {
				for _, key := range []string{"inputTokens", "outputTokens", "totalTokens", "inputCostUsd", "imageCostUsd", "outputCostUsd", "totalCostUsd"} {
					if _, ok := row[key]; !ok {
						t.Fatalf("%s missing token/cost transparency field %q: %#v", path, key, row)
					}
				}
				for _, key := range []string{"baseline", "baselineCostUsd", "savingsUsd", "savingsPct"} {
					if _, ok := row[key]; ok {
						t.Fatalf("%s exposed baseline/savings field %q by default: %#v", path, key, row)
					}
				}
			}
			if strings.Contains(path, "anomalies") {
				for _, key := range []string{"baseline", "baselineCostUsd", "savingsUsd", "savingsPct"} {
					if _, ok := row[key]; ok {
						t.Fatalf("%s exposed non-baseline savings field %q: %#v", path, key, row)
					}
				}
				if strings.HasPrefix(fmt.Sprint(row["key"]), "key-active") {
					t.Fatalf("%s treated active key state as anomalous: %#v", path, row)
				}
			}
			if strings.Contains(path, "latency-throughput") && row["avgUpstreamTokensPerSec"].(float64) <= 0 {
				t.Fatalf("%s missing throughput scalar fields: %#v", path, row)
			}
			if strings.Contains(path, "latency-throughput") {
				for _, key := range []string{"avgUpstreamOutputTokensPerSec", "avgUpstreamTotalTokensPerSec", "avgDownstreamWriteOutputTokensPerSec", "avgDownstreamWriteTotalTokensPerSec"} {
					if _, ok := row[key].(float64); !ok {
						t.Fatalf("%s missing explicit throughput field %q: %#v", path, key, row)
					}
				}
				if row["avgDownstreamWriteOutputTokensPerSec"].(float64) <= 0 {
					t.Fatalf("%s missing downstream write output throughput: %#v", path, row)
				}
			}
			if strings.Contains(path, "capability-usage") {
				if row["secondaryKey"] == "" {
					t.Fatalf("%s missing capability secondary group: %#v", path, row)
				}
			}
		}
		for _, forbidden := range []string{"token_sha256", "provider-key", testToken, "messages"} {
			if strings.Contains(rr.Body.String(), forbidden) {
				t.Fatalf("%s leaked %q: %s", path, forbidden, rr.Body.String())
			}
		}
	}

	forbiddenScalar := httptest.NewRequest(http.MethodGet, "/admin/reports/api/provider-model-mix?since=24h", nil)
	forbiddenScalar.Header.Set("Authorization", "Bearer "+testToken)
	forbiddenScalarRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(forbiddenScalarRR, forbiddenScalar)
	if forbiddenScalarRR.Code != http.StatusForbidden || !strings.Contains(forbiddenScalarRR.Body.String(), "reports-forbidden") {
		t.Fatalf("ordinary scalar report status=%d body=%s", forbiddenScalarRR.Code, forbiddenScalarRR.Body.String())
	}

	filtered := httptest.NewRequest(http.MethodGet, "/admin/reports/api/requests?since=24h&caller_id=alice&caller_ip=203.0.113.10&requested_model=default&provider=mock&target_model=mock-model&dialect=openai&status=504&cache=hit&limit=1", nil)
	filtered.SetBasicAuth("admin", "yell-yell-yum")
	filteredRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(filteredRR, filtered)
	if filteredRR.Code != http.StatusOK {
		t.Fatalf("filtered requests status=%d body=%s", filteredRR.Code, filteredRR.Body.String())
	}
	filteredBody := mustJSONMap(t, filteredRR.Body.String())
	filteredRequests := filteredBody["requests"].([]any)
	if len(filteredRequests) != 1 {
		t.Fatalf("filtered request rows len=%d body=%#v", len(filteredRequests), filteredBody)
	}
	filteredRow := filteredRequests[0].(map[string]any)
	for _, key := range []string{"callerId", "callerIp", "tokenId", "client", "requestedModel", "dialect", "cache", "attempts", "fallback", "latencyMs", "inputTokens", "outputTokens"} {
		if _, ok := filteredRow[key]; !ok {
			t.Fatalf("filtered request missing visible column field %q: %#v", key, filteredRow)
		}
	}
	if filteredRow["callerId"] != "alice" || filteredRow["callerIp"] != "203.0.113.10" || filteredRow["requestedModel"] != "default" || filteredRow["cache"] != "hit" {
		t.Fatalf("filtered request has wrong dimensions: %#v", filteredRow)
	}

	catalog := httptest.NewRequest(http.MethodGet, "/admin/reports/api/provider-catalog-status", nil)
	catalog.SetBasicAuth("admin", "yell-yell-yum")
	catalogRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(catalogRR, catalog)
	if catalogRR.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogRR.Code, catalogRR.Body.String())
	}
	catalogBody := mustJSONMap(t, catalogRR.Body.String())
	if catalogBody["summary"] == nil || len(catalogBody["rows"].([]any)) == 0 {
		t.Fatalf("catalog report missing summary or rows: %#v", catalogBody)
	}
	catalogRow := catalogBody["rows"].([]any)[0].(map[string]any)
	for _, key := range []string{"provider", "model", "dialect", "activeGroups", "validationStatus", "pricingMissing"} {
		if _, ok := catalogRow[key]; !ok {
			t.Fatalf("catalog row missing %q: %#v", key, catalogRow)
		}
	}
	foundEncodingMetadata := false
	for _, rawRow := range catalogBody["rows"].([]any) {
		row := rawRow.(map[string]any)
		if row["source"] == "active_target" && row["provider"] == "mock" && row["model"] == "mock-model" {
			if row["forceStoreFalse"] == true && row["outputTokenField"] == "max_completion_tokens" && row["honorsMaxTokens"] == true {
				foundEncodingMetadata = true
			}
		}
	}
	if !foundEncodingMetadata {
		t.Fatalf("catalog report missing active target encoding row: %#v", catalogBody["rows"])
	}
	for _, forbidden := range []string{"token_sha256", "provider-key", testToken, "api_key"} {
		if strings.Contains(catalogRR.Body.String(), forbidden) {
			t.Fatalf("catalog leaked %q: %s", forbidden, catalogRR.Body.String())
		}
	}

	retention := httptest.NewRequest(http.MethodGet, "/admin/reports/api/retention-status", nil)
	retention.SetBasicAuth("admin", "yell-yell-yum")
	retentionRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(retentionRR, retention)
	if retentionRR.Code != http.StatusOK {
		t.Fatalf("retention status=%d body=%s", retentionRR.Code, retentionRR.Body.String())
	}
	retentionBody := mustJSONMap(t, retentionRR.Body.String())
	if retentionBody["generatedUtc"] == "" || retentionBody["tables"] == nil || retentionBody["rollups"] == nil {
		t.Fatalf("retention report missing status shape: %#v", retentionBody)
	}

	custom := httptest.NewRequest(http.MethodGet, "/admin/reports/api/savings?since=24h&baseline=custom&baseline_input_price_per_million_usd=1&baseline_output_price_per_million_usd=2", nil)
	custom.SetBasicAuth("admin", "yell-yell-yum")
	customRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(customRR, custom)
	if customRR.Code != http.StatusOK || !strings.Contains(customRR.Body.String(), `"custom":true`) {
		t.Fatalf("custom savings status=%d body=%s", customRR.Code, customRR.Body.String())
	}

	badCustom := httptest.NewRequest(http.MethodGet, "/admin/reports/api/savings?since=24h&baseline=custom&baseline_input_price_per_million_usd=-1&baseline_output_price_per_million_usd=2", nil)
	badCustom.SetBasicAuth("admin", "yell-yell-yum")
	badCustomRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(badCustomRR, badCustom)
	if badCustomRR.Code != http.StatusBadRequest {
		t.Fatalf("bad custom savings status=%d body=%s", badCustomRR.Code, badCustomRR.Body.String())
	}

	requests := body["requests"].([]any)
	if len(requests) != 1 {
		t.Fatalf("summary request rows len=%d, want max_rows cap 1: %#v", len(requests), body)
	}
	requestID := "admin-report-synthetic-expensive"
	readerSummary := httptest.NewRequest(http.MethodGet, "/admin/reports/api/summary?since=24h", nil)
	readerSummary.SetBasicAuth("reader", "yell-yell-yum")
	readerSummaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(readerSummaryRR, readerSummary)
	if readerSummaryRR.Code != http.StatusOK {
		t.Fatalf("reader summary status=%d body=%s", readerSummaryRR.Code, readerSummaryRR.Body.String())
	}
	readerDetail := httptest.NewRequest(http.MethodGet, "/admin/reports/api/request/"+url.PathEscape(requestID), nil)
	readerDetail.SetBasicAuth("reader", "yell-yell-yum")
	readerDetailRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(readerDetailRR, readerDetail)
	if readerDetailRR.Code != http.StatusForbidden || !strings.Contains(readerDetailRR.Body.String(), "reports-forbidden") {
		t.Fatalf("reader detail status=%d body=%s", readerDetailRR.Code, readerDetailRR.Body.String())
	}
	readerEvidence := httptest.NewRequest(http.MethodGet, "/admin/reports/api/request-evidence?request_id="+url.QueryEscape(requestID), nil)
	readerEvidence.SetBasicAuth("reader", "yell-yell-yum")
	readerEvidenceRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(readerEvidenceRR, readerEvidence)
	if readerEvidenceRR.Code != http.StatusForbidden || !strings.Contains(readerEvidenceRR.Body.String(), "reports-forbidden") {
		t.Fatalf("reader evidence status=%d body=%s", readerEvidenceRR.Code, readerEvidenceRR.Body.String())
	}

	detail := httptest.NewRequest(http.MethodGet, "/admin/reports/api/request/"+url.PathEscape(requestID), nil)
	detail.SetBasicAuth("admin", "yell-yell-yum")
	detailRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(detailRR, detail)
	if detailRR.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRR.Code, detailRR.Body.String())
	}
	if detailRR.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("detail missing no-store header: %#v", detailRR.Header())
	}
	var detailBody map[string]any
	if err := json.Unmarshal(detailRR.Body.Bytes(), &detailBody); err != nil {
		t.Fatal(err)
	}
	attempts := detailBody["attempts"].([]any)
	if len(attempts) == 0 || attempts[0].(map[string]any)["attemptIndex"] == nil {
		t.Fatalf("detail missing safe attempt DTO fields: %#v", detailBody)
	}
	if attempts[0].(map[string]any)["retryAfterMs"].(float64) != 1500 {
		t.Fatalf("detail missing attempt retry-after evidence: %#v", attempts[0])
	}
	decisionTelemetry := detailBody["decisionTelemetry"].(map[string]any)
	for _, key := range []string{"shapeFeatures", "candidates", "filterReasons", "routingDecisions", "routingSignals", "dynamicScoreTerms", "policyExecutions", "cacheReasons"} {
		rows, ok := decisionTelemetry[key].([]any)
		if !ok || len(rows) == 0 {
			t.Fatalf("detail decision telemetry missing %s: %#v", key, decisionTelemetry)
		}
	}
	candidate := decisionTelemetry["candidates"].([]any)[0].(map[string]any)
	if candidate["contextTokens"].(float64) != 8192 || candidate["toolSupport"] != true || candidate["structuredOutput"] != true {
		t.Fatalf("detail candidate metadata missing phase-2 fields: %#v", candidate)
	}
	if detailBody["diagnosticCompleteness"] != "partial_missing" {
		t.Fatalf("detail completeness=%#v, want partial_missing: %#v", detailBody["diagnosticCompleteness"], detailBody)
	}
	evidence := detailBody["evidenceBundle"].(map[string]any)
	cost := evidence["costAccounting"].(map[string]any)
	for key, want := range map[string]float64{
		"inputPricePerMillionUsd":            1.25,
		"outputPricePerMillionUsd":           2.50,
		"imageInputPricePerMillionTokensUsd": 0.75,
		"inputCostUsd":                       1.25,
		"imageCostUsd":                       0.25,
		"outputCostUsd":                      1.25,
		"totalCostUsd":                       2.75,
		"upstreamReportedInputCostUsd":       1.30,
		"upstreamReportedOutputCostUsd":      1.45,
		"upstreamReportedTotalCostUsd":       2.95,
	} {
		if got := cost[key].(float64); got != want {
			t.Fatalf("cost %s=%v, want %v: %#v", key, got, want, cost)
		}
	}
	if cost["storedRequestTimeValues"] != true || cost["unknownPricing"] == true || cost["pricingSource"] != "https://example.test/pricing" {
		t.Fatalf("cost evidence missing request-time accounting metadata: %#v", cost)
	}
	admission := evidence["admissionAndShaping"].(map[string]any)
	if admission["quotaState"] != "soft_limit" || admission["keyState"] != "ok" || admission["cache"] != "hit" {
		t.Fatalf("admission evidence missing safe state: %#v", admission)
	}
	target := evidence["targetEligibility"].(map[string]any)
	if target["selectedProvider"] != "mock" || target["candidateCount"].(float64) != 1 || target["filterReasonCount"].(float64) != 1 {
		t.Fatalf("target evidence missing candidate summary: %#v", target)
	}
	completeness := evidence["completeness"].(map[string]any)
	if completeness["status"] != "partial_missing" || completeness["score"].(float64) <= 0 || completeness["score"].(float64) >= 100 {
		t.Fatalf("unexpected completeness summary: %#v", completeness)
	}
	var sawMissingShape bool
	for _, sectionAny := range completeness["sections"].([]any) {
		section := sectionAny.(map[string]any)
		if section["name"] == "request_shape" && section["status"] == "missing" {
			sawMissingShape = true
		}
	}
	if !sawMissingShape {
		t.Fatalf("completeness did not mark missing request shape evidence: %#v", completeness)
	}
	privacy := evidence["privacyBoundaries"].([]any)
	if len(privacy) == 0 || privacy[0].(string) != "no raw prompts" {
		t.Fatalf("evidence privacy boundary missing: %#v", privacy)
	}
	evidenceReq := httptest.NewRequest(http.MethodGet, "/admin/reports/api/request-evidence?request_id="+url.QueryEscape(requestID), nil)
	evidenceReq.SetBasicAuth("admin", "yell-yell-yum")
	evidenceRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(evidenceRR, evidenceReq)
	if evidenceRR.Code != http.StatusOK {
		t.Fatalf("evidence status=%d body=%s", evidenceRR.Code, evidenceRR.Body.String())
	}
	if evidenceRR.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("evidence missing no-store header: %#v", evidenceRR.Header())
	}
	evidenceBody := mustJSONMap(t, evidenceRR.Body.String())
	if evidenceBody["diagnosticCompleteness"] != "partial_missing" || evidenceBody["request"].(map[string]any)["requestId"] != requestID {
		t.Fatalf("query evidence endpoint returned wrong request evidence: %#v", evidenceBody)
	}
	for _, forbidden := range []string{"TokenSHA256", "token_sha256", "provider-key", testToken, "messages"} {
		if strings.Contains(detailRR.Body.String(), forbidden) {
			t.Fatalf("detail leaked %q: %s", forbidden, detailRR.Body.String())
		}
		if strings.Contains(evidenceRR.Body.String(), forbidden) {
			t.Fatalf("evidence leaked %q: %s", forbidden, evidenceRR.Body.String())
		}
	}
	crossDomainDetail := httptest.NewRequest(http.MethodGet, "/admin/reports/api/request/admin-report-cross-domain", nil)
	crossDomainDetail.SetBasicAuth("admin", "yell-yell-yum")
	crossDomainDetailRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(crossDomainDetailRR, crossDomainDetail)
	if crossDomainDetailRR.Code != http.StatusNotFound {
		t.Fatalf("cross-domain detail status=%d body=%s", crossDomainDetailRR.Code, crossDomainDetailRR.Body.String())
	}
	globalSummary := httptest.NewRequest(http.MethodGet, "/admin/reports/api/summary?since=24h", nil)
	globalSummary.SetBasicAuth("global", "yell-yell-yum")
	globalSummaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(globalSummaryRR, globalSummary)
	if globalSummaryRR.Code != http.StatusOK {
		t.Fatalf("global summary status=%d body=%s", globalSummaryRR.Code, globalSummaryRR.Body.String())
	}
	globalSummaryBody := mustJSONMap(t, globalSummaryRR.Body.String())
	if globalSummaryBody["summary"].(map[string]any)["requests"].(float64) <= body["summary"].(map[string]any)["requests"].(float64) {
		t.Fatalf("global summary did not include additional cross-domain rows: scoped=%#v global=%#v", body["summary"], globalSummaryBody["summary"])
	}
	globalDetail := httptest.NewRequest(http.MethodGet, "/admin/reports/api/request/admin-report-cross-domain", nil)
	globalDetail.SetBasicAuth("global", "yell-yell-yum")
	globalDetailRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(globalDetailRR, globalDetail)
	if globalDetailRR.Code != http.StatusOK {
		t.Fatalf("global detail status=%d body=%s", globalDetailRR.Code, globalDetailRR.Body.String())
	}

	ui := httptest.NewRequest(http.MethodGet, "/admin/reports/", nil)
	ui.SetBasicAuth("admin", "yell-yell-yum")
	uiRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(uiRR, ui)
	uiBody := uiRR.Body.String()
	for _, want := range []string{"Metrum AI Router Admin Reports", `id="root"`, `type="module"`, "./static/assets/admin-", ".js", ".css"} {
		if !strings.Contains(uiBody, want) {
			t.Fatalf("ui missing %q: status=%d body=%s", want, uiRR.Code, uiBody)
		}
	}
	if uiRR.Code != http.StatusOK || strings.Contains(uiBody, "https://") {
		t.Fatalf("ui status=%d body=%s", uiRR.Code, uiRR.Body.String())
	}

	asset := httptest.NewRequest(http.MethodGet, "/admin/reports/static/chart.umd.js", nil)
	asset.SetBasicAuth("admin", "yell-yell-yum")
	assetRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(assetRR, asset)
	if assetRR.Code != http.StatusOK || !strings.Contains(assetRR.Body.String(), "window.Chart") {
		t.Fatalf("asset status=%d body=%s", assetRR.Code, assetRR.Body.String())
	}
	for _, want := range []string{"window.hideChartTooltip", `querySelectorAll(".chart-tooltip")`, "pointerleave", `window.addEventListener("blur"`} {
		if !strings.Contains(assetRR.Body.String(), want) {
			t.Fatalf("chart asset missing tooltip singleton/dismiss contract %q: %s", want, assetRR.Body.String())
		}
	}

	css := httptest.NewRequest(http.MethodGet, "/admin/reports/static/admin.css", nil)
	css.SetBasicAuth("admin", "yell-yell-yum")
	cssRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(cssRR, css)
	if cssRR.Code != http.StatusOK || !strings.Contains(cssRR.Body.String(), "--metrum-purple: #cc28af") || strings.Contains(cssRR.Body.String(), "#1f6feb") {
		t.Fatalf("css status=%d body=%s", cssRR.Code, cssRR.Body.String())
	}
	for _, want := range []string{".sort-button", "cursor: pointer", ".sort-button:hover", ".sort-button:focus-visible"} {
		if !strings.Contains(cssRR.Body.String(), want) {
			t.Fatalf("css missing sortable header affordance %q: %s", want, cssRR.Body.String())
		}
	}

	js := httptest.NewRequest(http.MethodGet, "/admin/reports/static/admin.js", nil)
	js.SetBasicAuth("admin", "yell-yell-yum")
	jsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(jsRR, js)
	if jsRR.Code != http.StatusOK || !strings.Contains(jsRR.Body.String(), "metrum-admin-reports-theme") || !strings.Contains(jsRR.Body.String(), "localStorage") {
		t.Fatalf("js status=%d body=%s", jsRR.Code, jsRR.Body.String())
	}
	for _, want := range []string{"formatUnit", "renderChartSpecs", "color_key", "api/savings", "renderSharedTable", "sort-button", "aria-pressed", "dismissChartTooltip", "window.hideChartTooltip"} {
		if !strings.Contains(jsRR.Body.String(), want) {
			t.Fatalf("js missing chart contract helper %q: %s", want, jsRR.Body.String())
		}
	}
	for _, removed := range []string{"function aggregateRows(", "function requestRows(", "function savingsRows("} {
		if strings.Contains(jsRR.Body.String(), removed) {
			t.Fatalf("admin.js still has legacy non-sortable table builder %q: %s", removed, jsRR.Body.String())
		}
	}
	if strings.Contains(jsRR.Body.String(), "function hideChartTooltip(") {
		t.Fatalf("admin.js must not shadow chart.umd.js window.hideChartTooltip helper: %s", jsRR.Body.String())
	}
	for _, want := range []string{"Total Tokens", "Input Tokens", "Output Tokens", "Downstream write output tok/s", "avgDownstreamWriteTotalTokensPerSec", "requestedModel", "catalogColumns", "retentionColumns", "troubleshooting-buckets"} {
		if !strings.Contains(jsRR.Body.String(), want) {
			t.Fatalf("js missing transparent report label/field %q: %s", want, jsRR.Body.String())
		}
	}

	logo := httptest.NewRequest(http.MethodGet, "/admin/reports/static/metrum_logo_white_new.png", nil)
	logo.SetBasicAuth("admin", "yell-yell-yum")
	logoRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(logoRR, logo)
	if logoRR.Code != http.StatusOK || logoRR.Body.Len() == 0 {
		t.Fatalf("logo status=%d len=%d", logoRR.Code, logoRR.Body.Len())
	}

	exportReq := httptest.NewRequest(http.MethodGet, "/admin/reports/export.md?since=24h", nil)
	exportReq.SetBasicAuth("admin", "yell-yell-yum")
	exportRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(exportRR, exportReq)
	if exportRR.Code != http.StatusOK || !strings.Contains(exportRR.Body.String(), "# Metrum AI Router Usage Report") {
		t.Fatalf("export status=%d body=%s", exportRR.Code, exportRR.Body.String())
	}
	for _, want := range []string{"Total Tokens", "Input Tokens", "Output Tokens", "Downstream Write Output tok/s", "Upstream Total tok/s"} {
		if !strings.Contains(exportRR.Body.String(), want) {
			t.Fatalf("export missing transparent report label %q: %s", want, exportRR.Body.String())
		}
	}
}

func TestAdminReportQueryFailuresLogSafeStructuredContext(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		requestID string
		want      map[string]any
	}{
		{
			name:      "scalar aggregate report",
			path:      "/admin/reports/api/provider-model-mix?since=24h&limit=7&sort=costUsd&direction=asc",
			requestID: "admin-report-test-scalar",
			want: map[string]any{
				"event_type":       "admin_report_query_failed",
				"report":           "provider-model-mix",
				"handler":          "handleAdminScalarEndpoint",
				"db_driver":        "sqlite",
				"admin_request_id": "admin-report-test-scalar",
				"limit":            float64(7),
				"sort":             "cost",
				"direction":        "asc",
			},
		},
		{
			name:      "request page report",
			path:      "/admin/reports/api/requests?since=24h&limit=3&sort=latencyMs&direction=desc",
			requestID: "admin-report-test-requests",
			want: map[string]any{
				"event_type":       "admin_report_query_failed",
				"report":           "requests",
				"handler":          "handleAdminReportRequests",
				"db_driver":        "sqlite",
				"admin_request_id": "admin-report-test-requests",
				"limit":            float64(3),
				"sort":             "latencyMs",
				"direction":        "desc",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, logPath := newAdminReportFailureLoggingService(t, true)
			defer svc.Close()
			closeUsageDBForAdminReportFailureTest(t, svc)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.SetBasicAuth("admin", "yell-yell-yum")
			req.Header.Set("X-Request-Id", tc.requestID)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if rr.Body.String() != `{"error":{"message":"report-query-failed","type":"report-query-failed"}}`+"\n" {
				t.Fatalf("caller response changed or leaked detail: %s", rr.Body.String())
			}

			event := readSingleAdminReportFailureLogEvent(t, logPath)
			for key, want := range tc.want {
				if got := event[key]; got != want {
					t.Fatalf("event[%s]=%#v, want %#v; event=%#v", key, got, want, event)
				}
			}
			if event["from"] == "" || event["to"] == "" || event["error_class"] == "" || event["error_message"] == "" {
				t.Fatalf("event missing required scalar context: %#v", event)
			}
			assertAdminReportFailureLogSanitized(t, event)
		})
	}
}

func TestAdminReportMarkdownQueryFailureLogsSafeStructuredContext(t *testing.T) {
	svc, logPath := newAdminReportFailureLoggingService(t, true)
	defer svc.Close()
	closeUsageDBForAdminReportFailureTest(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/admin/reports/export.md?since=24h&limit=5", nil)
	req.SetBasicAuth("admin", "yell-yell-yum")
	req.Header.Set("X-Request-Id", "admin-report-test-markdown")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	event := readSingleAdminReportFailureLogEvent(t, logPath)
	for key, want := range map[string]any{
		"event_type":       "admin_report_query_failed",
		"report":           "markdown-export",
		"handler":          "handleAdminReportMarkdown",
		"db_driver":        "sqlite",
		"admin_request_id": "admin-report-test-markdown",
		"limit":            float64(5),
		"sort":             "timeUtc",
		"direction":        "desc",
	} {
		if got := event[key]; got != want {
			t.Fatalf("event[%s]=%#v, want %#v; event=%#v", key, got, want, event)
		}
	}
	assertAdminReportFailureLogSanitized(t, event)
}

func TestAdminReportQueryFailureLogsWrappedPostgresDiagnostics(t *testing.T) {
	svc, logPath := newAdminReportFailureLogOnlyService(t, "postgres")
	req := httptest.NewRequest(http.MethodGet, "/admin/reports/api/traffic-shaping-overview", nil)
	req.Header.Set("X-Request-Id", "pg-report-test")
	filters := &adminReportFilters{
		From:      time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:        time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC),
		Limit:     10,
		Sort:      "requests",
		Direction: "desc",
	}
	err := fmt.Errorf("admin report query failed: %w", &pgconn.PgError{
		Severity: "ERROR",
		Code:     "42803",
		Message:  "non-integer constant in GROUP BY",
		Detail:   "unsafe detail should not be logged",
		Hint:     "unsafe hint should not be logged",
	})

	svc.logAdminReportQueryFailure(req, "traffic-shaping-overview", "handleAdminScalarEndpoint", filters, err)

	event := readSingleAdminReportFailureLogEvent(t, logPath)
	for key, want := range map[string]any{
		"event_type":       "admin_report_query_failed",
		"report":           "traffic-shaping-overview",
		"handler":          "handleAdminScalarEndpoint",
		"db_driver":        "postgres",
		"admin_request_id": "pg-report-test",
		"error_class":      "*pgconn.PgError",
		"pg_code":          "42803",
		"pg_severity":      "ERROR",
		"pg_message":       "non-integer constant in GROUP BY",
		"limit":            float64(10),
		"sort":             "requests",
		"direction":        "desc",
	} {
		if got := event[key]; got != want {
			t.Fatalf("event[%s]=%#v, want %#v; event=%#v", key, got, want, event)
		}
	}
	if event["error_message"] == "" || event["from"] == "" || event["to"] == "" {
		t.Fatalf("event missing bounded safe diagnostic context: %#v", event)
	}
	if _, ok := event["pg_detail"]; ok {
		t.Fatalf("pg_detail should be omitted by default: %#v", event)
	}
	if _, ok := event["pg_hint"]; ok {
		t.Fatalf("pg_hint should be omitted by default: %#v", event)
	}
	assertAdminReportFailureLogSanitized(t, event)
}

func TestAdminReportQueryFailureLogsGenericSafeMessage(t *testing.T) {
	svc, logPath := newAdminReportFailureLogOnlyService(t, "postgres")
	req := httptest.NewRequest(http.MethodGet, "/admin/reports/api/provider-model-mix", nil)
	req.Header.Set("X-Request-Id", "generic-report-test")
	err := errors.New("query failed for Bearer rtr_abcdefghijklmnopqrstuvwxyz: api_key=sk-provider-secret-123456")

	svc.logAdminReportQueryFailure(req, "provider-model-mix", "handleAdminScalarEndpoint", nil, err)

	event := readSingleAdminReportFailureLogEvent(t, logPath)
	if got, want := event["error_class"], "*errors.errorString"; got != want {
		t.Fatalf("error_class=%#v, want %#v; event=%#v", got, want, event)
	}
	msg, _ := event["error_message"].(string)
	for _, forbidden := range []string{"rtr_abcdefghijklmnopqrstuvwxyz", "sk-provider-secret-123456", "provider-secret"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("generic error message leaked %q: %#v", forbidden, event)
		}
	}
	for _, want := range []string{"Bearer [REDACTED]", "api_key=[REDACTED]"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("generic error message missing %q: %#v", want, event)
		}
	}
	if len(msg) > 512 {
		t.Fatalf("generic error message length=%d, want <=512", len(msg))
	}
	assertAdminReportFailureLogSanitized(t, event)
}

func TestAdminReportQueryFailureRedactsRawSQLAndValues(t *testing.T) {
	svc, logPath := newAdminReportFailureLogOnlyService(t, "postgres")
	req := httptest.NewRequest(http.MethodGet, "/admin/reports/api/requests", nil)
	rawSQL := "SELECT * FROM request_usage WHERE caller_token = 'rtr_abcdefghijklmnopqrstuvwxyz' AND prompt = 'summarize payroll for jane@example.test'"
	err := errors.New("query failed: " + rawSQL)

	svc.logAdminReportQueryFailure(req, "requests", "handleAdminReportRequests", nil, err)

	event := readSingleAdminReportFailureLogEvent(t, logPath)
	msg, _ := event["error_message"].(string)
	if !strings.Contains(msg, "[REDACTED_SQL]") {
		t.Fatalf("SQL-shaped diagnostic was not redacted: %#v", event)
	}
	for _, forbidden := range []string{"SELECT ", " FROM ", " WHERE ", "caller_token", "rtr_abcdefghijklmnopqrstuvwxyz", "summarize payroll", "jane@example.test"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("SQL-shaped diagnostic leaked %q: %#v", forbidden, event)
		}
	}
	if len(msg) > 512 {
		t.Fatalf("SQL-shaped error message length=%d, want <=512", len(msg))
	}
	assertAdminReportFailureLogSanitized(t, event)
}

func newAdminReportFailureLogOnlyService(t *testing.T, driver string) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	logger, err := newRequestLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := logger.Close(); err != nil {
			t.Fatalf("close logger: %v", err)
		}
	})
	return &Service{
		cfg: &Config{Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"},
			UsageDB: UsageDBConfig{Driver: driver},
		}},
		logger: logger,
	}, logPath
}

func newAdminReportFailureLoggingService(t *testing.T, exportMarkdown bool) (*Service, string) {
	t.Helper()
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_REPORT_FAILURE_PASSWORD_HASH", hash)
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		Realm:             "Unit Test Admin",
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_REPORT_FAILURE_PASSWORD_HASH",
			Subject:         "basic:admin",
			Domain:          "metrum-insights/test",
		}},
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Policy: []string{
			"p, basic:admin, metrum-insights/test, admin:reports, read|export|drilldown",
		},
	}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100, ExportMarkdown: exportMarkdown}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc, cfg.Server.Logging.Path
}

func closeUsageDBForAdminReportFailureTest(t *testing.T, svc *Service) {
	t.Helper()
	sqlDB, err := svc.usage.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
}

func readSingleAdminReportFailureLogEvent(t *testing.T, logPath string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("log line count=%d raw=%s", len(lines), raw)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("decode log event: %v raw=%s", err, raw)
	}
	return event
}

func assertAdminReportFailureLogSanitized(t *testing.T, event map[string]any) {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{
		testToken,
		"provider-key",
		"yell-yell-yum",
		"Basic ",
		"Authorization",
		"token_sha256",
		"SELECT ",
		" FROM ",
		" WHERE ",
		"messages",
		"prompt",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("admin report failure log leaked %q: %s", forbidden, text)
		}
	}
}

func TestAdminSavingsUsesStoredRequestTimeActualCost(t *testing.T) {
	var agg adminSavingsAgg
	baseline := adminSavingsBaselineDTO{
		BaselineID:                       "fixed-baseline",
		BaselineInputPricePerMillionUSD:  1,
		BaselineOutputPricePerMillionUSD: 2,
	}
	agg.add(usageRow{
		InputTokens:              1_000_000,
		OutputTokens:             1_000_000,
		InputPricePerMillionUSD:  99,
		OutputPricePerMillionUSD: 199,
		InputCostUSD:             0.10,
		OutputCostUSD:            0.32,
		TotalCostUSD:             0.42,
	}, baseline)
	row := agg.row("default")
	if math.Abs(row.ActualCostUSD-0.42) > 0.0000001 {
		t.Fatalf("actual cost=%v, want stored total_cost_usd 0.42", row.ActualCostUSD)
	}
	if math.Abs(row.BaselineCostUSD-3.0) > 0.0000001 || math.Abs(row.SavingsUSD-2.58) > 0.0000001 {
		t.Fatalf("baseline/savings calculation mismatch: %#v", row)
	}
}

func TestAdminCatalogStatusSeparatesCatalogAndActiveTargetMetadata(t *testing.T) {
	cfg := Config{
		Provider: map[string]ProviderConfig{
			"mock": {
				Dialect: "openai-chat",
				Models: map[string]ProviderModel{
					"shared": {
						Model:                    "mock-vlm",
						DisplayName:              "Mock VLM",
						ContextTokens:            16384,
						InputModalities:          []string{"text", "image"},
						OutputModalities:         []string{"text"},
						ToolSupport:              ToolSupport{OpenAIChat: []string{"auto"}},
						InputPricePerMillionUSD:  1,
						OutputPricePerMillionUSD: 2,
						PricingSource:            "test",
						PricingUpdatedAt:         "2026-06-26",
						HonorsMaxTokens:          boolPtr(true),
					},
				},
			},
		},
		Models: map[string]ModelGroup{
			"text-group": {
				Targets: []Target{{
					Provider:        "mock",
					ModelRef:        "shared",
					InputModalities: []string{"text"},
					Validation: &TargetValidation{
						Status:      "passed",
						Workload:    "text-smoke",
						ValidatedAt: "2026-06-26T00:00:00Z",
						Harness:     "unit",
					},
				}},
			},
			"vision-group": {
				Targets: []Target{{Provider: "mock", ModelRef: "shared"}},
			},
		},
	}

	resp := buildAdminCatalogStatusResponse(cfg)
	if resp.Summary.CatalogModels != 1 || resp.Summary.ActiveTargets != 2 || resp.Summary.ValidatedTargets != 1 || resp.Summary.PassedTargets != 1 {
		t.Fatalf("unexpected catalog summary: %#v", resp.Summary)
	}

	var catalogRow, textTarget, visionTarget *adminCatalogStatusRow
	for i := range resp.Rows {
		row := &resp.Rows[i]
		switch {
		case row.Source == "catalog":
			catalogRow = row
		case row.Source == "active_target" && len(row.ActiveGroups) == 1 && row.ActiveGroups[0] == "text-group":
			textTarget = row
		case row.Source == "active_target" && len(row.ActiveGroups) == 1 && row.ActiveGroups[0] == "vision-group":
			visionTarget = row
		}
	}
	if catalogRow == nil || textTarget == nil || visionTarget == nil {
		t.Fatalf("missing expected catalog/active rows: %#v", resp.Rows)
	}
	if !stringSliceEqual(catalogRow.InputModalities, []string{"image", "text"}) || catalogRow.ActiveTargetCount != 0 {
		t.Fatalf("catalog row should preserve catalog metadata without active target counts: %#v", catalogRow)
	}
	if !stringSliceEqual(textTarget.InputModalities, []string{"text"}) || textTarget.ValidationStatus != "passed" {
		t.Fatalf("text active target should preserve target override and validation: %#v", textTarget)
	}
	if !stringSliceEqual(visionTarget.InputModalities, []string{"image", "text"}) || visionTarget.ValidationStatus != "missing" {
		t.Fatalf("unvalidated active target should keep resolved catalog metadata without inherited validation: %#v", visionTarget)
	}
}

func TestAdminCatalogStatusReportsEffectiveProviderSkinEligibility(t *testing.T) {
	cfg := Config{
		Provider: map[string]ProviderConfig{
			"chat_skin": {
				Dialect: "chat",
				Models: map[string]ProviderModel{
					"shared": {
						Model:           "shared-upstream-model",
						InputModalities: []string{"text", "image"},
						ToolSupport: ToolSupport{
							OpenAIChat:        []string{"tools", "structured_outputs"},
							OpenAIResponses:   []string{"function"},
							AnthropicMessages: []string{"client_tools"},
						},
						Reasoning: ReasoningSupport{Supported: true, Control: reasoningControlEffortEnum},
					},
				},
			},
			"responses_skin": {
				Dialect: "responses",
				Models: map[string]ProviderModel{
					"shared": {
						Model: "shared-upstream-model",
						ToolSupport: ToolSupport{
							OpenAIResponses: []string{"function", "structured_outputs"},
						},
					},
				},
			},
		},
		Models: map[string]ModelGroup{
			"agent-group": {
				Strategy: "weighted",
				Targets: []Target{
					{Provider: "chat_skin", ModelRef: "shared", Weight: 1},
					{Provider: "responses_skin", ModelRef: "shared", Weight: 1, ToolOnly: true},
				},
			},
		},
	}

	resp := buildAdminCatalogStatusResponse(cfg)
	if resp.Summary.ActiveTargets != 2 || resp.Summary.TargetsWithInactiveSkins != 1 || resp.Summary.InactiveMetadataSurfaces != 2 {
		t.Fatalf("unexpected effective eligibility summary: %#v", resp.Summary)
	}
	if len(resp.GroupSummary) != 1 {
		t.Fatalf("expected one group summary: %#v", resp.GroupSummary)
	}
	group := resp.GroupSummary[0]
	if group.Group != "agent-group" ||
		group.OpenAIChatTargets != 1 ||
		group.OpenAIChatToolTargets != 1 ||
		group.OpenAIChatStructuredOutputTargets != 1 ||
		group.OpenAIChatImageTargets != 1 ||
		group.OpenAIChatReasoningTargets != 1 ||
		group.OpenAIResponsesTargets != 0 ||
		group.OpenAIResponsesToolTargets != 1 ||
		group.OpenAIResponsesStructuredTargets != 1 {
		t.Fatalf("unexpected group effective eligibility: %#v", group)
	}

	var chatTarget, responsesTarget *adminCatalogStatusRow
	for i := range resp.Rows {
		row := &resp.Rows[i]
		if row.Source != "active_target" {
			continue
		}
		switch row.Provider {
		case "chat_skin":
			chatTarget = row
		case "responses_skin":
			responsesTarget = row
		}
	}
	if chatTarget == nil || responsesTarget == nil {
		t.Fatalf("missing active target rows: %#v", resp.Rows)
	}
	if chatTarget.ActiveEligibilitySkin != "native:openai-chat" ||
		!stringSliceEqual(chatTarget.EffectiveToolSupport, []string{"openai_chat:structured_outputs", "openai_chat:tools"}) ||
		!stringSliceEqual(chatTarget.InactiveToolSupport, []string{"anthropic_messages:client_tools", "openai_responses:function"}) ||
		chatTarget.EligibilityWarning != "metadata-for-inactive-provider-skin" ||
		!chatTarget.EffectiveStructured ||
		!chatTarget.EffectiveReasoning ||
		!chatTarget.EffectiveImageInput {
		t.Fatalf("chat target should expose active and inactive skin metadata separately: %#v", chatTarget)
	}
	if responsesTarget.ActiveEligibilitySkin != "native:openai-responses" ||
		!stringSliceEqual(responsesTarget.EffectiveToolSupport, []string{"openai_responses:function", "openai_responses:structured_outputs"}) ||
		len(responsesTarget.InactiveToolSupport) != 0 ||
		!responsesTarget.EffectiveStructured {
		t.Fatalf("responses target should expose only native responses metadata as effective: %#v", responsesTarget)
	}
}

func stringSliceEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type fakeOIDCIssuer struct {
	server    *httptest.Server
	key       *rsa.PrivateKey
	nextNonce atomic.Value
	nextEmail atomic.Value
}

func newFakeOIDCIssuer(t *testing.T) *fakeOIDCIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &fakeOIDCIssuer{key: key}
	issuer.nextEmail.Store("alice@example.com")
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                issuer.server.URL,
			"authorization_endpoint":                issuer.server.URL + "/authorize",
			"token_endpoint":                        issuer.server.URL + "/token",
			"jwks_uri":                              issuer.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{issuer.jwk()}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") == "" || r.Form.Get("code_verifier") == "" {
			t.Fatalf("unexpected token request form: %#v", r.Form)
		}
		nonce, _ := issuer.nextNonce.Load().(string)
		email, _ := issuer.nextEmail.Load().(string)
		idToken := issuer.signIDToken(t, map[string]any{
			"iss":            issuer.server.URL,
			"sub":            "stable-subject-1",
			"aud":            "test-client-id",
			"exp":            time.Now().Add(time.Hour).Unix(),
			"iat":            time.Now().Add(-time.Minute).Unix(),
			"nonce":          nonce,
			"email":          email,
			"email_verified": true,
			"groups":         []string{"admins", "finance"},
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "opaque-access-token",
			"id_token":     idToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	issuer.server = httptest.NewServer(mux)
	return issuer
}

func (i *fakeOIDCIssuer) Close() {
	i.server.Close()
}

func (i *fakeOIDCIssuer) URL() string {
	return i.server.URL
}

func (i *fakeOIDCIssuer) jwk() map[string]any {
	n := base64.RawURLEncoding.EncodeToString(i.key.PublicKey.N.Bytes())
	e := big.NewInt(int64(i.key.PublicKey.E)).Bytes()
	return map[string]any{
		"kty": "RSA",
		"use": "sig",
		"kid": "test-key",
		"alg": "RS256",
		"n":   n,
		"e":   base64.RawURLEncoding.EncodeToString(e),
	}
}

func (i *fakeOIDCIssuer) signIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"typ": "JWT", "alg": "RS256", "kid": "test-key"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	unsigned := encodedHeader + "." + encodedClaims
	sum := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func newTestOIDCAdminService(t *testing.T, issuer *fakeOIDCIssuer, policy []string) *Service {
	t.Helper()
	t.Setenv("TEST_OIDC_CLIENT_ID", "test-client-id")
	t.Setenv("TEST_OIDC_CLIENT_SECRET", "client-secret")
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminAuth.OIDC = AdminOIDCConfig{
		Enabled:         true,
		IssuerURL:       issuer.URL(),
		ClientIDEnv:     "TEST_OIDC_CLIENT_ID",
		ClientSecretEnv: "TEST_OIDC_CLIENT_SECRET",
		RedirectURL:     "http://localhost/admin/auth/callback",
		AllowedDomains:  []string{"example.com"},
		GroupsClaim:     "groups",
		EmailClaim:      "email",
		SubjectClaim:    "email",
		Domain:          "example/prod",
	}
	cfg.Server.AdminAuth.Sessions = AdminSessionConfig{
		CookieName:    "test_admin_session",
		TTL:           time.Hour,
		SecureCookies: testBoolPtr(false),
		SameSite:      "lax",
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: policy}
	if len(policy) == 0 {
		cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, user:nobody@example.com, example/prod, admin:reports, read"}}
	}
	cfg.Server.AdminReports.Enabled = true
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func loginTestOIDCAdmin(t *testing.T, svc *Service, issuer *fakeOIDCIssuer) *http.Cookie {
	t.Helper()
	login := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	loginRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(loginRR, login)
	if loginRR.Code != http.StatusFound {
		t.Fatalf("login status=%d body=%s", loginRR.Code, loginRR.Body.String())
	}
	authURL, err := url.Parse(loginRR.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	issuer.nextNonce.Store(authURL.Query().Get("nonce"))
	callback := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state="+url.QueryEscape(authURL.Query().Get("state"))+"&code=ok", nil)
	callbackRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(callbackRR, callback)
	if callbackRR.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", callbackRR.Code, callbackRR.Body.String())
	}
	cookies := callbackRR.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("callback cookies=%d", len(cookies))
	}
	return cookies[0]
}

func testBoolPtr(v bool) *bool {
	return &v
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAdminCapabilityUsageReportsEverySignal(t *testing.T) {
	rows := buildAdminScalarReportResponse(
		adminReportFilters{
			From:  time.Now().UTC().Add(-time.Hour),
			To:    time.Now().UTC(),
			Limit: 20,
		},
		[]usageRow{{
			RequestID:        "req_capability",
			TS:               time.Now().UTC(),
			ResolvedGroup:    "default",
			TargetDialect:    "openai-responses",
			Stream:           true,
			Cache:            "hit",
			InputHasImage:    true,
			InputImageCount:  1,
			InputImageTokens: 12,
			PIIFilterApplied: true,
			InputTokens:      10,
			OutputTokens:     5,
			TotalTokens:      15,
			Status:           200,
			Attempts:         1,
		}},
		adminScalarEndpointSpec{Report: "capability-usage", Dimension: "capability", Secondary: "model_group", Sort: "requests"},
		adminSavingsBaselineDTO{},
	).Rows
	got := map[string]bool{}
	for _, row := range rows {
		got[row.Key] = true
	}
	for _, want := range []string{"image-input", "streaming", "pii-filtered", "cacheable", "dialect:openai-responses"} {
		if !got[want] {
			t.Fatalf("missing capability %q in %#v", want, rows)
		}
	}
}

func TestAdminAdaptiveBackoffReportFiltersProviderCapacitySkips(t *testing.T) {
	now := time.Now().UTC()
	rows := buildAdminShapeReportResponse(
		adminReportFilters{
			From:  now.Add(-time.Hour),
			To:    now,
			Limit: 20,
		},
		[]usageRow{{RequestID: "req_shape", TS: now, TargetProvider: "mock", TargetModel: "mock-model"}},
		[]upstreamShapeJoinedEvent{
			{
				Row: usageRow{RequestID: "req_shape", TS: now, TargetProvider: "mock", TargetModel: "mock-model"},
				Event: requestUpstreamShapeEventRecord{
					RequestID:     "req_shape",
					Seq:           1,
					Provider:      "mock",
					Model:         "mock-model",
					Dialect:       "openai-chat",
					Bucket:        shapeBucketRequestStart,
					Decision:      shapeDecisionSkipped,
					BackoffReason: "provider-shape-throttled",
				},
			},
			{
				Row: usageRow{RequestID: "req_shape", TS: now, TargetProvider: "mock", TargetModel: "mock-model"},
				Event: requestUpstreamShapeEventRecord{
					RequestID:     "req_shape",
					Seq:           2,
					Provider:      "mock",
					Model:         "mock-model",
					Dialect:       "openai-chat",
					Bucket:        shapeBucketBackoff,
					Decision:      shapeDecisionCooldownStarted,
					BackoffReason: "adaptive-backoff-provider-429",
				},
			},
		},
		adminScalarEndpointSpec{Report: "adaptive-upstream-backoff", Dimension: "backoff_reason", Secondary: "provider_model", ShapeReport: "adaptive"},
	).Rows
	if len(rows) != 1 {
		t.Fatalf("adaptive rows=%d: %#v", len(rows), rows)
	}
	if rows[0].Key != "adaptive-backoff-provider-429" || rows[0].SkippedTargets != 0 || rows[0].CooldownsStarted != 1 {
		t.Fatalf("unexpected adaptive row: %#v", rows[0])
	}
}

func TestAdminAnomalyKeysTreatActiveKeyStateAsNormal(t *testing.T) {
	spec := adminScalarEndpointSpec{Secondary: "provider_model"}
	keys := adminAnomalyKeys(usageRow{
		TargetProvider: "mock",
		TargetModel:    "mock-model",
		KeyState:       "active",
	}, spec)
	if len(keys) != 0 {
		t.Fatalf("active key state produced anomalies: %#v", keys)
	}

	keys = adminAnomalyKeys(usageRow{
		TargetProvider: "mock",
		TargetModel:    "mock-model",
		KeyState:       "disabled",
	}, spec)
	if len(keys) != 1 || keys[0].Key != "key-disabled" {
		t.Fatalf("disabled key state did not produce key-disabled anomaly: %#v", keys)
	}
}

func TestAdminDerivedBucketReportsUseSQLAggregates(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	rateLimitErr := "rate limit max_tokens quota"
	for _, rec := range []logRecord{
		{
			TS:                base.Format(time.RFC3339),
			RequestID:         "derived-a",
			CallerID:          "alice",
			CallerProject:     "local",
			CallerEnvironment: "test",
			TokenID:           "rtr_derived_a",
			RequestedModel:    "default",
			ResolvedGroup:     "default",
			TargetProvider:    "mock",
			TargetModel:       "mock-a",
			TargetDialect:     "openai-chat",
			Stream:            true,
			Cache:             "hit",
			Status:            http.StatusTooManyRequests,
			Attempts:          2,
			FallbackUsed:      true,
			LatencyMS:         35000,
			Usage:             Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			InputHasImage:     true,
			InputImageCount:   1,
			InputImageTokens:  20,
			TotalCostUSD:      2,
			QuotaState:        "exhausted",
			KeyState:          "active",
			Error:             &rateLimitErr,
		},
		{
			TS:                base.Add(time.Minute).Format(time.RFC3339),
			RequestID:         "derived-b",
			CallerID:          "alice",
			CallerProject:     "local",
			CallerEnvironment: "test",
			TokenID:           "rtr_derived_b",
			RequestedModel:    "default",
			ResolvedGroup:     "default",
			TargetProvider:    "mock",
			TargetModel:       "mock-b",
			Cache:             "bypass",
			Status:            http.StatusOK,
			Attempts:          1,
			LatencyMS:         120,
			Usage:             Usage{InputTokens: 4, OutputTokens: 1, TotalTokens: 5},
			PIIFilterApplied:  true,
			QuotaState:        "ok",
			KeyState:          "disabled",
		},
		{
			TS:                base.Add(2 * time.Minute).Format(time.RFC3339),
			RequestID:         "derived-c",
			CallerID:          "alice",
			CallerProject:     "local",
			CallerEnvironment: "test",
			TokenID:           "rtr_derived_c",
			RequestedModel:    "default",
			ResolvedGroup:     "default",
			TargetProvider:    "mock",
			TargetModel:       "mock-c",
			Cache:             "none",
			Status:            http.StatusOK,
			Attempts:          1,
			LatencyMS:         90,
			Usage:             Usage{InputTokens: 3, OutputTokens: 1, TotalTokens: 4},
			QuotaState:        "ok",
			KeyState:          "ok",
		},
	} {
		svc.usage.Emit(rec)
	}

	for _, tc := range []struct {
		path    string
		want    []string
		notWant []string
	}{
		{
			path:    "/admin/reports/api/troubleshooting-buckets?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=50",
			want:    []string{"quota:exhausted", "rpm-rate-limit", "max-token-or-context", "cache-hit", "cache-bypass", "fallback", "multi-attempt", "client-error", "key:disabled", "ok"},
			notWant: []string{"key:active"},
		},
		{
			path:    "/admin/reports/api/capability-usage?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=50",
			want:    []string{"image-input", "streaming", "cacheable", "dialect:openai-chat", "pii-filtered", "text"},
			notWant: []string{"dialect:unknown"},
		},
		{
			path:    "/admin/reports/api/anomalies?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=50",
			want:    []string{"error", "fallback", "multi-attempt", "slow-request", "expensive-request", "quota-exhausted", "key-disabled"},
			notWant: []string{"key-active"},
		},
	} {
		report := adminReportJSON(t, svc, tc.path)
		summary := report["summary"].(map[string]any)
		if summary["requests"].(float64) != 3 {
			t.Fatalf("%s summary counted bucketed rows instead of requests: %#v", tc.path, summary)
		}
		keys := adminReportRowKeys(report["rows"].([]any))
		for _, want := range tc.want {
			if !keys[want] {
				t.Fatalf("%s missing key %q in %#v", tc.path, want, keys)
			}
		}
		for _, notWant := range tc.notWant {
			if keys[notWant] {
				t.Fatalf("%s unexpected key %q in %#v", tc.path, notWant, keys)
			}
		}
		page := report["pagination"].(map[string]any)
		if page["mode"] != "top_n" || page["has_more"] != false {
			t.Fatalf("%s unexpected pagination metadata: %#v", tc.path, page)
		}
	}
}

func adminReportRowKeys(rows []any) map[string]bool {
	keys := make(map[string]bool, len(rows))
	for _, raw := range rows {
		row := raw.(map[string]any)
		keys[fmt.Sprint(row["key"])] = true
	}
	return keys
}

func TestAdminReportRequestCursorPagination(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"req-a", "req-b", "req-c", "req-d", "req-e"} {
		svc.usage.Emit(logRecord{
			TS:                base.Add(time.Duration(i/2) * time.Minute).Format(time.RFC3339),
			RequestID:         id,
			CallerID:          "alice",
			CallerUser:        "alice",
			CallerProject:     "local",
			CallerEnvironment: "test",
			TokenID:           "rtr_local_" + strconv.Itoa(i),
			Client:            "codex-cli",
			RequestedModel:    "default",
			ResolvedGroup:     "default",
			TargetProvider:    "mock",
			TargetModel:       "mock-model",
			TargetDialect:     "openai-chat",
			Cache:             "miss",
			Status:            200,
			Attempts:          1,
			LatencyMS:         int64(100 + i),
			Usage:             Usage{InputTokens: 10 + i, OutputTokens: 2, TotalTokens: 12 + i},
			TotalCostUSD:      float64(i + 1),
			QuotaState:        "ok",
			KeyState:          "ok",
		})
	}
	svc.usage.Emit(logRecord{
		TS:                base.Add(3 * time.Minute).Format(time.RFC3339),
		RequestID:         "req-other-domain",
		CallerID:          "mallory",
		CallerProject:     "other",
		CallerEnvironment: "prod",
		TokenID:           "rtr_other",
		RequestedModel:    "default",
		ResolvedGroup:     "default",
		TargetProvider:    "mock",
		TargetModel:       "mock-model",
		TargetDialect:     "openai-chat",
		Cache:             "miss",
		Status:            200,
		Attempts:          1,
		QuotaState:        "ok",
		KeyState:          "ok",
	})

	first := adminReportJSON(t, svc, "/admin/reports/api/requests?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=timeUtc&direction=desc")
	firstPage := first["pagination"].(map[string]any)
	if firstPage["mode"] != "cursor" || firstPage["returned"].(float64) != 2 || firstPage["total_count"].(float64) != 5 || firstPage["has_more"] != true {
		t.Fatalf("unexpected first page metadata: %#v", firstPage)
	}
	firstRequests := first["requests"].([]any)
	if got := requestIDsFromAdminRows(firstRequests); strings.Join(got, ",") != "req-e,req-d" {
		t.Fatalf("first page order=%v", got)
	}
	nextCursor := firstPage["next_cursor"].(string)
	if nextCursor == "" {
		t.Fatalf("missing next cursor: %#v", firstPage)
	}

	second := adminReportJSON(t, svc, "/admin/reports/api/requests?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=timeUtc&direction=desc&cursor="+url.QueryEscape(nextCursor))
	secondRequests := second["requests"].([]any)
	if got := requestIDsFromAdminRows(secondRequests); strings.Join(got, ",") != "req-c,req-b" {
		t.Fatalf("second page order=%v body=%#v", got, second)
	}
	if strings.Contains(fmt.Sprint(second), "req-other-domain") {
		t.Fatalf("domain-scoped second page leaked other domain row: %#v", second)
	}

	asc := adminReportJSON(t, svc, "/admin/reports/api/requests?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=timeUtc&direction=asc")
	if got := requestIDsFromAdminRows(asc["requests"].([]any)); strings.Join(got, ",") != "req-a,req-b" {
		t.Fatalf("asc page order=%v", got)
	}

	bad := httptest.NewRequest(http.MethodGet, "/admin/reports/api/requests?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&cursor=tampered", nil)
	bad.SetBasicAuth("admin", "yell-yell-yum")
	badRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(badRR, bad)
	if badRR.Code != http.StatusBadRequest || !strings.Contains(badRR.Body.String(), "invalid-report-filter") {
		t.Fatalf("tampered cursor status=%d body=%s", badRR.Code, badRR.Body.String())
	}
}

func TestAdminReportExpensiveRequestsAndTopNMetadata(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for i, cost := range []float64{1, 4, 9} {
		svc.usage.Emit(logRecord{
			TS:                base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			RequestID:         fmt.Sprintf("cost-req-%d", i),
			CallerID:          "alice",
			CallerProject:     "local",
			CallerEnvironment: "test",
			TokenID:           fmt.Sprintf("rtr_cost_%d", i),
			RequestedModel:    "default",
			ResolvedGroup:     "default",
			TargetProvider:    "mock",
			TargetModel:       "mock-model",
			TargetDialect:     "openai-chat",
			Cache:             "miss",
			Status:            200,
			Attempts:          1,
			LatencyMS:         int64(100 + i),
			Usage:             Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
			TotalCostUSD:      cost,
			QuotaState:        "ok",
			KeyState:          "ok",
		})
	}

	expensive := adminReportJSON(t, svc, "/admin/reports/api/expensive-requests?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=costUsd&direction=desc")
	page := expensive["pagination"].(map[string]any)
	if page["mode"] != "cursor" || page["sort"] != "costUsd" || page["has_more"] != true || page["total_count"].(float64) != 3 {
		t.Fatalf("expensive pagination metadata=%#v", page)
	}
	if got := requestIDsFromAdminRows(expensive["requests"].([]any)); strings.Join(got, ",") != "cost-req-2,cost-req-1" {
		t.Fatalf("expensive order=%v", got)
	}

	carriedSort := adminReportJSON(t, svc, "/admin/reports/api/expensive-requests?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=outcome&direction=desc")
	carriedSortPage := carriedSort["pagination"].(map[string]any)
	if carriedSortPage["sort"] != "costUsd" {
		t.Fatalf("carried-over sort did not fall back to expensive default: %#v", carriedSortPage)
	}

	topN := adminReportJSON(t, svc, "/admin/reports/api/usage-by-key?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=1")
	topNPage := topN["pagination"].(map[string]any)
	if topNPage["mode"] != "top_n" || topNPage["returned"].(float64) != 1 || topNPage["total_count"] != nil || topNPage["has_more"] != true || topNPage["note"] == "" {
		t.Fatalf("top-n pagination metadata=%#v", topNPage)
	}

	topNCarriedSort := adminReportJSON(t, svc, "/admin/reports/api/usage-by-key?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=1&sort=requestId")
	topNCarriedSortPage := topNCarriedSort["pagination"].(map[string]any)
	if topNCarriedSortPage["mode"] != "top_n" {
		t.Fatalf("top-N carried-over sort should still load report: %#v", topNCarriedSortPage)
	}
}

func TestAdminMarkdownExportUsesBoundedRecentRows(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	svc.cfg.Server.AdminReports.ExportMarkdown = true
	base := time.Date(2026, 6, 20, 12, 0, 0, 987654321, time.UTC)
	costs := []float64{999, 2, 3}
	for i, tokenID := range []string{"rtr_export_old", "rtr_export_mid", "rtr_export_new"} {
		svc.usage.Emit(logRecord{
			TS:                base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
			RequestID:         fmt.Sprintf("export-req-%d", i),
			CallerID:          "export-user",
			CallerUser:        "export@example.com",
			CallerProject:     "local",
			CallerEnvironment: "test",
			TokenID:           tokenID,
			RequestedModel:    "big-coder",
			ResolvedGroup:     "big-coder",
			TargetProvider:    "mock",
			TargetModel:       "mock-model",
			TargetDialect:     "openai-chat",
			Cache:             "miss",
			Status:            200,
			Attempts:          1,
			LatencyMS:         int64(100 + i),
			Usage:             Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
			TotalCostUSD:      costs[i],
			QuotaState:        "ok",
			KeyState:          "ok",
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/reports/export.md?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=costUsd", nil)
	req.SetBasicAuth("admin", "yell-yell-yum")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", rr.Code, body)
	}
	if !strings.Contains(body, "| 2026-06-20T12:01:00.987654321Z |") {
		t.Fatalf("browser Markdown export lost existing timestamp precision: %s", body)
	}
	if !strings.Contains(body, "Export scope: showing the most recent 2 request rows") {
		t.Fatalf("export missing bounded scope note: %s", body)
	}
	if strings.Contains(body, "rtr_export_old") {
		t.Fatalf("export included row outside bounded recent sample: %s", body)
	}
	for _, want := range []string{"rtr_export_mid", "rtr_export_new"} {
		if !strings.Contains(body, want) {
			t.Fatalf("export missing bounded row %q: %s", want, body)
		}
	}
}

func TestAdminMarkdownSummaryExportUsesSQLTotalsAndTopN(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	svc.cfg.Server.AdminReports.ExportMarkdown = true
	if err := svc.usage.db.Callback().Query().Before("gorm:query").Register("test:forbid_summary_raw_usage_records", func(tx *gorm.DB) {
		switch tx.Statement.Dest.(type) {
		case *[]usageRecord, []usageRecord:
			tx.AddError(errors.New("summary export must not materialize raw usage rows"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	fixtures := []struct {
		id     string
		group  string
		caller string
		tokens int
		cost   float64
		status int
	}{
		{"summary-old-secret-req", "group-a", "caller-a", 100, 1000, 200},
		{"summary-mid-secret-req", "group-b", "caller-b", 20, 2, 200},
		{"summary-new-secret-req", "group-c", "caller-c", 30, 3, 500},
	}
	for i, fixture := range fixtures {
		var errorText *string
		if fixture.status >= 400 {
			v := "upstream-failed"
			errorText = &v
		}
		svc.usage.Emit(logRecord{
			TS:                base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			RequestID:         fixture.id,
			CallerID:          fixture.caller,
			CallerUser:        "summary@example.com",
			CallerProject:     "local",
			CallerEnvironment: "test",
			TokenID:           fmt.Sprintf("rtr_summary_secret_%d", i),
			Client:            "codex",
			InboundDialect:    "openai-responses",
			RequestedModel:    fixture.group,
			ResolvedGroup:     fixture.group,
			TargetProvider:    "mock",
			TargetModel:       "mock-model",
			TargetDialect:     "openai-chat",
			Cache:             "miss",
			Status:            fixture.status,
			Attempts:          1,
			LatencyMS:         int64(100 + i),
			Usage:             Usage{InputTokens: fixture.tokens, OutputTokens: 1, TotalTokens: fixture.tokens + 1},
			TotalCostUSD:      fixture.cost,
			QuotaState:        "ok",
			KeyState:          "ok",
			Error:             errorText,
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/reports/export.md?mode=summary&from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2", nil)
	req.SetBasicAuth("admin", "yell-yell-yum")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("summary export status=%d body=%s", rr.Code, body)
	}
	for _, want := range []string{
		"# Metrum AI Router Usage Summary",
		"Mode: `summary`",
		"Scope: full-window SQL totals with bounded top-N aggregate sections; no raw request rows are included.",
		"Requests: `3`",
		"Errors: `1`",
		"Total Tokens: `153`; Input Tokens: `150`; Output Tokens: `3`",
		"Cost: `$1005.000000` total",
		"## Top Model Groups By Requests",
		"Top 2 aggregate rows ranked by `requests`",
		"More aggregate buckets matched the selected window.",
		"## Top Provider Models By Tokens",
		"## Top Callers By Cost",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary export missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "group-c") {
		t.Fatalf("summary top-N section included third model-group bucket despite limit=2:\n%s", body)
	}
	for _, forbidden := range []string{"summary-old-secret-req", "summary-mid-secret-req", "summary-new-secret-req", "rtr_summary_secret_"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("summary export leaked raw row/token value %q:\n%s", forbidden, body)
		}
	}
}

func TestAdminMarkdownExportRejectsUnknownMode(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	svc.cfg.Server.AdminReports.ExportMarkdown = true
	req := httptest.NewRequest(http.MethodGet, "/admin/reports/export.md?mode=everything", nil)
	req.SetBasicAuth("admin", "yell-yell-yum")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown mode status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid-report-filter") {
		t.Fatalf("unknown mode did not return safe filter error: %s", rr.Body.String())
	}
}

func TestAdminSavingsAggregateSQLTopNUsesFullWindowSummary(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for i, rec := range []struct {
		user string
		cost float64
	}{
		{user: "alice@example.com", cost: 1},
		{user: "bob@example.com", cost: 2},
		{user: "carol@example.com", cost: 3},
	} {
		svc.usage.Emit(logRecord{
			TS:                base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			RequestID:         fmt.Sprintf("savings-sql-%d", i),
			CallerID:          rec.user,
			CallerUser:        rec.user,
			CallerProject:     "local",
			CallerEnvironment: "test",
			TokenID:           fmt.Sprintf("rtr_savings_sql_%d", i),
			RequestedModel:    "big-coder",
			ResolvedGroup:     "big-coder",
			TargetProvider:    "mock",
			TargetModel:       "mock-model",
			TargetDialect:     "openai-chat",
			Cache:             "miss",
			Status:            200,
			Attempts:          1,
			LatencyMS:         int64(100 + i),
			Usage:             Usage{InputTokens: 1_000_000, OutputTokens: 0, TotalTokens: 1_000_000},
			InputCostUSD:      rec.cost,
			TotalCostUSD:      rec.cost,
			QuotaState:        "ok",
			KeyState:          "ok",
		})
	}

	report := adminReportJSON(t, svc, "/admin/reports/api/savings-by-user?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=1&baseline=custom&baseline_input_price_per_million_usd=10&baseline_output_price_per_million_usd=0&sort=savings")
	rows := report["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows len=%d, want exactly top row: %#v", len(rows), rows)
	}
	row := rows[0].(map[string]any)
	if row["key"] != "alice@example.com" {
		t.Fatalf("top savings row=%#v, want alice", row)
	}
	assertCloseFloat(t, "row actual cost", row["actualCostUsd"].(float64), 1)
	assertCloseFloat(t, "row baseline cost", row["baselineCostUsd"].(float64), 10)
	assertCloseFloat(t, "row savings", row["savingsUsd"].(float64), 9)

	summary := report["summary"].(map[string]any)
	assertCloseFloat(t, "summary actual cost", summary["actualCostUsd"].(float64), 6)
	assertCloseFloat(t, "summary baseline cost", summary["baselineCostUsd"].(float64), 30)
	assertCloseFloat(t, "summary savings", summary["savingsUsd"].(float64), 24)
	if summary["requests"].(float64) != 3 {
		t.Fatalf("summary requests=%#v, want full-window count 3", summary["requests"])
	}
	page := report["pagination"].(map[string]any)
	if page["mode"] != "top_n" || page["returned"].(float64) != 1 || page["has_more"] != true {
		t.Fatalf("pagination=%#v, want bounded top-N with has_more", page)
	}
}

func TestAdminOverviewSQLUsesFullWindowSummaryAndBoundedSample(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	ttfb := int64(20)
	upstream := int64(70)
	downstream := int64(10)
	upstreamTPS := 42.5
	downstreamTPS := 38.25
	for i, rec := range []struct {
		id     string
		caller string
		cost   float64
		status int
		cache  string
	}{
		{id: "overview-old", caller: "alice", cost: 1, status: 200, cache: "hit"},
		{id: "overview-mid", caller: "alice", cost: 2, status: 502, cache: "miss"},
		{id: "overview-new", caller: "alice", cost: 9, status: 200, cache: "bypass"},
		{id: "overview-other", caller: "bob", cost: 100, status: 200, cache: "hit"},
	} {
		svc.usage.Emit(logRecord{
			TS:                  base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			RequestID:           rec.id,
			CallerID:            rec.caller,
			CallerUser:          rec.caller + "@example.com",
			CallerProject:       "local",
			CallerEnvironment:   "test",
			TokenID:             fmt.Sprintf("rtr_overview_%d", i),
			Client:              "codex-cli",
			RequestedModel:      "default",
			ResolvedGroup:       "default",
			TargetProvider:      "mock",
			TargetModel:         "mock-model",
			TargetDialect:       "openai-chat",
			Cache:               rec.cache,
			Status:              rec.status,
			Attempts:            1,
			FallbackUsed:        rec.status >= 500,
			LatencyMS:           int64(100 + i),
			TTFBMS:              &ttfb,
			UpstreamMS:          &upstream,
			DownstreamMS:        &downstream,
			UpstreamOutputTPS:   &upstreamTPS,
			UpstreamTotalTPS:    &upstreamTPS,
			DownstreamOutputTPS: &downstreamTPS,
			DownstreamTotalTPS:  &downstreamTPS,
			Usage:               Usage{InputTokens: 1_000_000, OutputTokens: 0, TotalTokens: 1_000_000},
			InputCostUSD:        rec.cost,
			TotalCostUSD:        rec.cost,
			CacheEnabled:        true,
			CacheItems:          int64(10 + i),
			CacheBytes:          int64(100 + i),
			CacheMaxBytes:       1000,
			CacheOccupancyPct:   float64(10 + i),
			QuotaState:          "ok",
			KeyState:            "ok",
		})
	}

	report := adminReportJSON(t, svc, "/admin/reports/api/overview?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&caller_id=alice&limit=1&baseline=custom&baseline_input_price_per_million_usd=10&baseline_output_price_per_million_usd=0")
	summary := report["summary"].(map[string]any)
	if summary["requests"].(float64) != 3 || summary["errors"].(float64) != 1 {
		t.Fatalf("overview summary did not use full filtered window: %#v", summary)
	}
	assertCloseFloat(t, "overview actual cost", summary["actualCostUsd"].(float64), 12)
	assertCloseFloat(t, "overview baseline cost", summary["baselineCostUsd"].(float64), 30)
	assertCloseFloat(t, "overview savings", summary["savingsUsd"].(float64), 18)
	requests := report["requests"].([]any)
	if len(requests) != 1 || requests[0].(map[string]any)["requestId"] != "overview-new" {
		t.Fatalf("overview recent sample=%#v, want only newest filtered request", requests)
	}
	page := report["pagination"].(map[string]any)
	if page["mode"] != "top_n" || page["returned"].(float64) != 1 || page["has_more"] != true {
		t.Fatalf("overview pagination=%#v", page)
	}
	cache := report["cache"].(map[string]any)
	if cache["latestItems"].(float64) != 12 || cache["latestBytes"].(float64) != 102 {
		t.Fatalf("overview cache should use latest filtered snapshot: %#v", cache)
	}
	chartIDs := map[string]bool{}
	for _, raw := range report["charts"].([]any) {
		chartIDs[raw.(map[string]any)["chart_id"].(string)] = true
	}
	for _, want := range []string{"requests", "cost", "cost_baseline_savings", "savings_pct", "latency", "throughput", "errors_fallbacks", "error_fallback_rates", "cache", "cache_hit_rate"} {
		if !chartIDs[want] {
			t.Fatalf("missing overview chart %q in %#v", want, chartIDs)
		}
	}
	series := report["series"].([]any)
	if len(series) != 1 {
		t.Fatalf("series len=%d, want one hourly bucket: %#v", len(series), series)
	}
	bucket := series[0].(map[string]any)
	if bucket["successes"].(float64) != 2 || bucket["errors"].(float64) != 1 {
		t.Fatalf("unexpected overview bucket counts: %#v", bucket)
	}
	assertCloseFloat(t, "bucket savings pct", bucket["savingsPct"].(float64), 60)
}

func TestAdminScalarSQLGroupByOmitsEmptySecondaryDimension(t *testing.T) {
	groupBy := adminScalarAggSQLGroupBy(adminScalarEndpointSpec{
		Report:    "usage-by-key",
		Dimension: "token_id",
	}, "COALESCE(NULLIF(token_id, ''), 'unknown')", "''")
	if groupBy != "COALESCE(NULLIF(token_id, ''), 'unknown')" {
		t.Fatalf("group by=%q, want only primary dimension", groupBy)
	}

	groupBy = adminScalarAggSQLGroupBy(adminScalarEndpointSpec{
		Report:    "usage-by-provider-model",
		Dimension: "provider_model",
		Secondary: "dialect",
	}, "target_provider || '/' || target_model", "target_dialect")
	if groupBy != "target_provider || '/' || target_model, target_dialect" {
		t.Fatalf("group by with secondary=%q", groupBy)
	}

	latencyOrder := adminScalarAggSQLOrder("latency", adminSavingsBaselineDTO{})
	if strings.Contains(latencyOrder, "avgLatency") || strings.Contains(latencyOrder, "avg_latency") || !strings.Contains(latencyOrder, "SUM(latency_ms)") {
		t.Fatalf("latency order should repeat aggregate expression instead of alias arithmetic: %q", latencyOrder)
	}
}

func assertCloseFloat(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("%s=%g, want %g", name, got, want)
	}
}

func TestAdminTroubleshootingReportsUseSafeDiagnosticTelemetry(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	rawProviderMessage := "Bad request: prompt contained customer SSN 123-45-6789"
	upstreamFailed := "upstream-failed"
	for i, rec := range []logRecord{
		{
			RequestID:         "diag-failed",
			CallerID:          "caller-a",
			CallerUser:        "william",
			CallerProject:     "local",
			CallerEnvironment: "test",
			Client:            "codex-cli",
			InboundDialect:    "openai-responses",
			RequestedModel:    "big-coder",
			ResolvedGroup:     "big-coder",
			TargetProvider:    "minimax",
			TargetModel:       "MiniMax-M3",
			TargetDialect:     "openai-responses",
			Stream:            true,
			Cache:             "bypass",
			Status:            502,
			Attempts:          2,
			FallbackUsed:      true,
			LatencyMS:         900,
			Usage:             Usage{InputTokens: 5000, OutputTokens: 20, TotalTokens: 5020},
			Error:             &upstreamFailed,
			QuotaState:        "ok",
			KeyState:          "ok",
			RequestShape: &requestShapeLogRecord{
				InboundDialect:             "openai-responses",
				RequestedModel:             "big-coder",
				ResolvedGroup:              "big-coder",
				Client:                     "codex-cli",
				Stream:                     true,
				ToolCount:                  4,
				ToolChoiceMode:             "auto",
				ReasoningPresent:           true,
				TotalRequestBytesBucket:    "bytes:large",
				EstimatedInputTokensBucket: "tokens:large",
				RequestedOutputCapBucket:   "output:large",
				RequestShapeFingerprint:    "shape_safe_fp",
				ToolSchemaFingerprint:      "tool_safe_fp",
			},
			TranslationShapes: []translationShapeLogRecord{{
				AttemptIndex:                 0,
				Provider:                     "minimax",
				Model:                        "MiniMax-M3",
				Dialect:                      "openai-responses",
				TranslatedStream:             true,
				TranslatedToolCount:          4,
				TranslatedToolChoiceMode:     "auto",
				TranslatedOutputCapBucket:    "output:large",
				TranslatedReasoningControl:   "reasoning:effort",
				TranslatedRequestBytesBucket: "bytes:large",
				UnsupportedFieldsPresent:     true,
				RequestShapeFingerprint:      "shape_safe_fp",
				ToolSchemaFingerprint:        "tool_safe_fp",
			}},
			AttemptsDetail: []attemptLogRecord{{
				Index:        0,
				StatusCode:   400,
				Provider:     "minimax",
				Model:        "MiniMax-M3",
				Dialect:      "openai-responses",
				ErrorClass:   "upstream-failed",
				ErrorMessage: "provider_message:invalid_request",
				ErrorDetails: []upstreamErrorDetailLogRecord{
					{Seq: 0, AttemptIndex: 0, StatusCode: 400, ErrorClass: "upstream-failed", FieldName: "code", FieldValue: "invalid_request", Source: "json"},
					{Seq: 1, AttemptIndex: 0, StatusCode: 400, ErrorClass: "upstream-failed", FieldName: "message", FieldValue: "provider_message:invalid_request", Source: "json"},
				},
			}},
			FallbackTransitions: []fallbackTransitionLogRecord{{
				Seq:                    0,
				AttemptIndex:           0,
				FailedProvider:         "minimax",
				FailedModel:            "MiniMax-M3",
				FailedDialect:          "openai-responses",
				FallbackProvider:       "baseten",
				FallbackModel:          "gpt-oss-120b",
				FallbackDialect:        "openai-responses",
				FallbackReason:         "upstream-failed",
				ErrorClass:             "upstream-failed",
				Retryable:              true,
				FallbackSucceeded:      false,
				FailedCandidateIndex:   0,
				FallbackCandidateIndex: 1,
			}},
		},
		{
			RequestID:         "diag-success",
			CallerID:          "caller-a",
			CallerUser:        "william",
			CallerProject:     "local",
			CallerEnvironment: "test",
			Client:            "codex-cli",
			InboundDialect:    "openai-responses",
			RequestedModel:    "big-coder",
			ResolvedGroup:     "big-coder",
			TargetProvider:    "baseten",
			TargetModel:       "gpt-oss-120b",
			TargetDialect:     "openai-responses",
			Stream:            true,
			Cache:             "bypass",
			Status:            200,
			Attempts:          2,
			FallbackUsed:      true,
			LatencyMS:         700,
			Usage:             Usage{InputTokens: 5000, OutputTokens: 200, TotalTokens: 5200},
			QuotaState:        "ok",
			KeyState:          "ok",
			RequestShape: &requestShapeLogRecord{
				InboundDialect:             "openai-responses",
				RequestedModel:             "big-coder",
				ResolvedGroup:              "big-coder",
				Client:                     "codex-cli",
				Stream:                     true,
				ToolCount:                  4,
				ToolChoiceMode:             "auto",
				ReasoningPresent:           true,
				TotalRequestBytesBucket:    "bytes:large",
				EstimatedInputTokensBucket: "tokens:large",
				RequestedOutputCapBucket:   "output:large",
				RequestShapeFingerprint:    "shape_safe_fp",
				ToolSchemaFingerprint:      "tool_safe_fp",
			},
			TranslationShapes: []translationShapeLogRecord{{
				AttemptIndex:                 0,
				Provider:                     "baseten",
				Model:                        "gpt-oss-120b",
				Dialect:                      "openai-responses",
				TranslatedStream:             true,
				TranslatedToolCount:          4,
				TranslatedToolChoiceMode:     "auto",
				TranslatedOutputCapBucket:    "output:large",
				TranslatedReasoningControl:   "reasoning:effort",
				TranslatedRequestBytesBucket: "bytes:large",
				RequestShapeFingerprint:      "shape_safe_fp",
				ToolSchemaFingerprint:        "tool_safe_fp",
			}},
			AttemptsDetail: []attemptLogRecord{{
				Index:        0,
				StatusCode:   400,
				Provider:     "minimax",
				Model:        "MiniMax-M3",
				Dialect:      "openai-responses",
				ErrorClass:   "upstream-failed",
				ErrorMessage: "provider_message:invalid_request",
				Retryable:    true,
			}},
		},
	} {
		rec.TS = base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		svc.usage.Emit(rec)
	}

	for _, path := range []string{
		"/admin/reports/api/upstream-failures?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=10",
		"/admin/reports/api/upstream-failures?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&status=400&limit=10",
		"/admin/reports/api/request-shape-failures?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=10",
		"/admin/reports/api/request-shape-mismatches?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=10",
		"/admin/reports/api/fallback-health?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=10",
		"/admin/reports/api/user-client-impact?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=10",
		"/admin/reports/api/client-impact?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=10",
	} {
		body := adminReportJSON(t, svc, path)
		if len(body["rows"].([]any)) == 0 || len(body["charts"].([]any)) == 0 {
			t.Fatalf("%s missing rows or charts: %#v", path, body)
		}
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), rawProviderMessage) || strings.Contains(string(encoded), "123-45-6789") {
			t.Fatalf("%s leaked raw provider text: %s", path, encoded)
		}
	}
	fallbackHealth := adminReportJSON(t, svc, "/admin/reports/api/fallback-health?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=10")
	var sawParentFallback bool
	for _, item := range fallbackHealth["rows"].([]any) {
		row := item.(map[string]any)
		if rate, ok := row["fallbackRatePct"].(float64); ok && rate > 100 {
			t.Fatalf("fallback health produced impossible fallback rate: %#v", row)
		}
		if row["fallbackSucceeded"] == float64(1) {
			sawParentFallback = true
		}
	}
	if !sawParentFallback {
		t.Fatalf("fallback health did not include parent-only fallback row in mixed telemetry: %#v", fallbackHealth)
	}
	shapeStatus := adminReportJSON(t, svc, "/admin/reports/api/request-shape-failures?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&status=400&limit=10")
	if rows, ok := shapeStatus["rows"].([]any); ok && len(rows) != 0 {
		t.Fatalf("status-filtered shape report included non-400 parent rows: %#v", shapeStatus)
	}

	detail := adminReportJSON(t, svc, "/admin/reports/api/request/diag-failed")
	upstream := detail["upstreamErrorTelemetry"].(map[string]any)
	if len(upstream["details"].([]any)) == 0 {
		t.Fatalf("request detail missing upstream error telemetry: %#v", detail)
	}
}

func TestAdminSecurityReportCursorPagination(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, true)
	defer svc.Close()
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"sec-a", "sec-b", "sec-c"} {
		svc.usage.EmitSecurityAccessEvent(securityAccessEvent{
			TS:                base.Add(time.Duration(i) * time.Second),
			RequestID:         id,
			EventType:         "api_auth_failed",
			Surface:           "v1_chat_completions",
			HTTPMethod:        http.MethodPost,
			PathTemplate:      "/v1/chat/completions",
			StatusCode:        http.StatusForbidden,
			Outcome:           "forbidden",
			ReasonCode:        "reports-forbidden",
			CallerProject:     "local",
			CallerEnvironment: "test",
			IPAddress:         "203.0.113." + strconv.Itoa(10+i),
		})
	}

	first := adminReportJSON(t, svc, "/admin/reports/api/security/events?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=timeUtc&direction=desc")
	page := first["pagination"].(map[string]any)
	if page["mode"] != "cursor" || page["returned"].(float64) != 2 || page["total_count"].(float64) != 3 || page["has_more"] != true {
		t.Fatalf("security page metadata=%#v", page)
	}
	rows := first["rows"].([]any)
	if got := securityRequestIDsFromAdminRows(rows); strings.Join(got, ",") != "sec-c,sec-b" {
		t.Fatalf("security order=%v", got)
	}
	carriedSort := adminReportJSON(t, svc, "/admin/reports/api/security/events?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=requestId&direction=desc")
	carriedSortPage := carriedSort["pagination"].(map[string]any)
	if carriedSortPage["sort"] != "timeUtc" {
		t.Fatalf("carried-over request sort did not fall back to security default: %#v", carriedSortPage)
	}
	next := page["next_cursor"].(string)
	second := adminReportJSON(t, svc, "/admin/reports/api/security/events?from=2026-06-20T11:00:00Z&to=2026-06-20T13:00:00Z&limit=2&sort=timeUtc&direction=desc&cursor="+url.QueryEscape(next))
	if got := securityRequestIDsFromAdminRows(second["rows"].([]any)); strings.Join(got, ",") != "sec-a" {
		t.Fatalf("security second page order=%v", got)
	}
}

func TestAdminOverviewIncludesAuthorizedSecurityTrendCharts(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, true)
	defer svc.Close()
	base := time.Date(2026, 6, 20, 12, 15, 0, 0, time.UTC)
	for _, event := range []securityAccessEvent{
		{
			TS:                base,
			RequestID:         "sec-allowed",
			EventType:         "admin_access",
			Surface:           "admin_reports",
			StatusCode:        http.StatusOK,
			Outcome:           "allowed",
			ReasonCode:        "",
			CallerProject:     "local",
			CallerEnvironment: "test",
		},
		{
			TS:                base.Add(30 * time.Minute),
			RequestID:         "sec-denied",
			EventType:         "api_auth_failed",
			Surface:           "v1_chat_completions",
			StatusCode:        http.StatusForbidden,
			Outcome:           "forbidden",
			ReasonCode:        "reports-forbidden",
			CallerProject:     "local",
			CallerEnvironment: "test",
		},
		{
			TS:                base,
			RequestID:         "sec-other-domain",
			EventType:         "api_auth_failed",
			Surface:           "v1_messages",
			StatusCode:        http.StatusUnauthorized,
			Outcome:           "unauthorized",
			ReasonCode:        "invalid-token",
			CallerProject:     "other",
			CallerEnvironment: "prod",
		},
	} {
		svc.usage.EmitSecurityAccessEvent(event)
	}

	resp := adminReportJSON(t, svc, "/admin/reports/api/overview?from=2026-06-20T12:00:00Z&to=2026-06-20T14:00:00Z&limit=50")
	charts := resp["charts"].([]any)
	events := jsonChartByID(t, charts, "security_events_over_time")
	series := jsonChartSeriesTotals(events)
	if series["allowed"] < 1 || series["forbidden"] != 1 {
		t.Fatalf("security event series=%#v", series)
	}
	if _, ok := series["unauthorized"]; ok {
		t.Fatalf("overview leaked out-of-domain unauthorized event: %#v", series)
	}
	denials := jsonChartByID(t, charts, "security_denials_over_time")
	denialSeries := jsonChartSeriesTotals(denials)
	if denialSeries["v1_chat_completions"] != 1 {
		t.Fatalf("security denial series=%#v", denialSeries)
	}
	if _, ok := denialSeries["v1_messages"]; ok {
		t.Fatalf("overview leaked out-of-domain denial surface: %#v", denialSeries)
	}
}

func TestAdminOverviewOmitsSecurityTrendChartsWithoutSecurityPermission(t *testing.T) {
	svc := newAdminReportPaginationTestService(t, false)
	defer svc.Close()
	svc.usage.EmitSecurityAccessEvent(securityAccessEvent{
		TS:                time.Date(2026, 6, 20, 12, 15, 0, 0, time.UTC),
		RequestID:         "sec-denied",
		EventType:         "api_auth_failed",
		Surface:           "v1_chat_completions",
		StatusCode:        http.StatusForbidden,
		Outcome:           "forbidden",
		ReasonCode:        "reports-forbidden",
		CallerProject:     "local",
		CallerEnvironment: "test",
	})

	resp := adminReportJSON(t, svc, "/admin/reports/api/overview?from=2026-06-20T12:00:00Z&to=2026-06-20T14:00:00Z&limit=50")
	for _, chart := range resp["charts"].([]any) {
		if strings.HasPrefix(chart.(map[string]any)["chart_id"].(string), "security_") {
			t.Fatalf("overview included security chart without security permission: %#v", chart)
		}
	}
}

func newAdminReportPaginationTestService(t *testing.T, security bool) *Service {
	t.Helper()
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "local/test",
		}},
	}
	policy := []string{"p, basic:admin, local/test, admin:reports, read|export|drilldown"}
	if security {
		policy = append(policy, "p, basic:admin, local/test, admin:security_reports, read|export")
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: policy}
	cfg.Server.AdminReports = AdminReportsConfig{
		Enabled:      true,
		DefaultSince: "24h",
		MaxRange:     "31d",
		MaxRows:      100,
		// These pagination fixtures use a fixed June 2026 reporting window. Keep
		// them inside the retention horizon as calendar time advances; retention
		// enforcement itself is covered by dedicated usage-store tests.
		Security: AdminSecurityReportsConfig{Enabled: security, RetentionDays: 365},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func adminReportJSON(t *testing.T, svc *Service, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetBasicAuth("admin", "yell-yell-yum")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, rr.Code, rr.Body.String())
	}
	return mustJSONMap(t, rr.Body.String())
}

func jsonChartByID(t *testing.T, charts []any, id string) map[string]any {
	t.Helper()
	for _, raw := range charts {
		chart := raw.(map[string]any)
		if chart["chart_id"] == id {
			return chart
		}
	}
	t.Fatalf("chart %q not found in %#v", id, charts)
	return nil
}

func jsonChartSeriesTotals(chart map[string]any) map[string]float64 {
	out := map[string]float64{}
	for _, rawSeries := range chart["series"].([]any) {
		series := rawSeries.(map[string]any)
		name := series["name"].(string)
		for _, rawPoint := range series["points"].([]any) {
			point := rawPoint.(map[string]any)
			out[name] += point["y"].(float64)
		}
	}
	return out
}

func requestIDsFromAdminRows(rows []any) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.(map[string]any)["requestId"].(string))
	}
	return out
}

func securityRequestIDsFromAdminRows(rows []any) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.(map[string]any)["requestId"].(string))
	}
	return out
}

func TestAdminReportsRejectWithoutCasbinPolicy(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "local/test",
		}},
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, Policy: []string{"p, other, local/test, admin:reports, read"}}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/admin/reports/api/summary", nil)
	req.SetBasicAuth("admin", "yell-yell-yum")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "reports-forbidden") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminSecurityReportsPersistSafeAccessEvents(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "up_security",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.ClientIP = ClientIPConfig{TrustedProxyCIDRs: []string{"192.0.2.0/24"}, HeaderOrder: []string{"X-Forwarded-For", "X-Real-IP"}}
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		Realm:             "Unit Test Admin",
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "admin",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:admin",
			Domain:          "local/test",
		}, {
			Username:        "reader",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:reader",
			Domain:          "local/test",
		}},
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Policy: []string{
			"g, basic:admin, reports_admin, local/test",
			"p, basic:reader, local/test, admin:security_reports, read",
			"p, reports_admin, local/test, admin:reports, read|export|drilldown",
			"p, reports_admin, local/test, admin:security_reports, read|export",
			"p, basic:admin, *, admin:security_reports, read|export",
		},
	}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 50, ExportMarkdown: true, Security: AdminSecurityReportsConfig{Enabled: true, RetentionDays: 30}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.usage.EmitSecurityAccessEvent(securityAccessEvent{
		TS:         time.Now().UTC().Add(-60 * 24 * time.Hour),
		RequestID:  "req_old_security",
		EventType:  "api_auth_failed",
		Surface:    "v1_chat_completions",
		StatusCode: http.StatusUnauthorized,
		Outcome:    "unauthorized",
		ReasonCode: "invalid-token",
		IPAddress:  "198.51.100.1",
	})
	svc.usage.EmitSecurityAccessEvent(securityAccessEvent{
		TS:              time.Now().UTC().Add(-time.Minute),
		RequestID:       "=req_formula",
		EventType:       "+event_formula",
		Surface:         "-surface_formula",
		HTTPMethod:      http.MethodGet,
		PathTemplate:    "@path_formula",
		StatusCode:      http.StatusForbidden,
		Outcome:         "forbidden",
		ReasonCode:      "=reason_formula",
		AuthSubject:     "+auth_formula",
		AuthSource:      "basic",
		CallerID:        "-caller_formula",
		CallerUser:      "@user_formula",
		CallerProject:   "=project_formula",
		TokenID:         "+token_formula",
		AdminSubject:    "-admin_formula",
		Client:          "@client_formula",
		UserAgentFamily: "ordinary, quoted",
		IPAddress:       "203.0.113.99",
		ModelGroup:      "=group_formula",
		RequestedModel:  "+requested_formula",
		ResolvedGroup:   "-resolved_formula",
	})

	invalid := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	invalid.RemoteAddr = "192.0.2.10:1234"
	invalid.Header.Set("X-Forwarded-For", "203.0.113.55, 192.0.2.10")
	invalid.Header.Set("Authorization", "Bearer invalid-secret-token")
	invalidRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(invalidRR, invalid)
	if invalidRR.Code != http.StatusUnauthorized {
		t.Fatalf("invalid status=%d body=%s", invalidRR.Code, invalidRR.Body.String())
	}

	okReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	okReq.Header.Set("Authorization", "Bearer "+testToken)
	okRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(okRR, okReq)
	if okRR.Code != http.StatusOK {
		t.Fatalf("ok status=%d body=%s", okRR.Code, okRR.Body.String())
	}
	usageReq := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	usageReq.Header.Set("Authorization", "Bearer "+testToken)
	usageRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(usageRR, usageReq)
	if usageRR.Code != http.StatusOK {
		t.Fatalf("usage status=%d body=%s", usageRR.Code, usageRR.Body.String())
	}

	ordinary := httptest.NewRequest(http.MethodGet, "/admin/reports/api/security/events?since=24h", nil)
	ordinary.Header.Set("Authorization", "Bearer "+testToken)
	ordinaryRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ordinaryRR, ordinary)
	if ordinaryRR.Code != http.StatusForbidden || !strings.Contains(ordinaryRR.Body.String(), "reports-forbidden") {
		t.Fatalf("ordinary security status=%d body=%s", ordinaryRR.Code, ordinaryRR.Body.String())
	}

	report := httptest.NewRequest(http.MethodGet, "/admin/reports/api/security/events?since=24h&limit=50", nil)
	report.SetBasicAuth("admin", "yell-yell-yum")
	reportRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(reportRR, report)
	if reportRR.Code != http.StatusOK {
		t.Fatalf("security report status=%d body=%s", reportRR.Code, reportRR.Body.String())
	}
	body := mustJSONMap(t, reportRR.Body.String())
	rows := body["rows"].([]any)
	if len(rows) == 0 {
		t.Fatalf("security report has no rows: %#v", body)
	}
	foundInvalid := false
	foundAllowed := false
	foundUsage := false
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["requestId"] == "req_old_security" {
			t.Fatalf("old security event was not purged: %#v", row)
		}
		if row["reason"] == "invalid-token" {
			foundInvalid = true
			if row["ipAddress"] != "203.0.113.55" || row["ipSource"] != "x-forwarded-for" || row["trustedProxyApplied"] != true {
				t.Fatalf("invalid-token row missing trusted proxy IP metadata: %#v", row)
			}
		}
		if row["outcome"] == "allowed" && row["surface"] == "v1_chat_completions" {
			foundAllowed = true
			if row["inputTokens"].(float64) <= 0 || row["outputTokens"].(float64) <= 0 {
				t.Fatalf("allowed row missing token split: %#v", row)
			}
		}
		if row["surface"] == "v1_usage" {
			foundUsage = true
			if row["method"] != http.MethodGet || row["path"] != "/v1/usage" {
				t.Fatalf("usage row missing method/path: %#v", row)
			}
		}
	}
	if !foundInvalid || !foundAllowed || !foundUsage {
		t.Fatalf("missing expected security rows invalid=%v allowed=%v usage=%v rows=%#v", foundInvalid, foundAllowed, foundUsage, rows)
	}
	readerCSV := httptest.NewRequest(http.MethodGet, "/admin/reports/security/export.csv?since=24h", nil)
	readerCSV.SetBasicAuth("reader", "yell-yell-yum")
	readerCSVRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(readerCSVRR, readerCSV)
	if readerCSVRR.Code != http.StatusForbidden || !strings.Contains(readerCSVRR.Body.String(), "reports-forbidden") {
		t.Fatalf("reader csv status=%d body=%s", readerCSVRR.Code, readerCSVRR.Body.String())
	}
	csvReq := httptest.NewRequest(http.MethodGet, "/admin/reports/security/export.csv?since=24h", nil)
	csvReq.SetBasicAuth("admin", "yell-yell-yum")
	csvRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(csvRR, csvReq)
	if csvRR.Code != http.StatusOK || !strings.Contains(csvRR.Body.String(), "input_tokens,output_tokens,total_tokens") {
		t.Fatalf("csv status=%d body=%s", csvRR.Code, csvRR.Body.String())
	}
	csvRows, err := csv.NewReader(strings.NewReader(csvRR.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v\n%s", err, csvRR.Body.String())
	}
	var formulaRow []string
	for _, row := range csvRows {
		if len(row) > 1 && row[1] == "'=req_formula" {
			formulaRow = row
			break
		}
	}
	if formulaRow == nil {
		t.Fatalf("missing formula-neutralized row: %#v", csvRows)
	}
	for _, idx := range []int{1, 2, 3, 5, 8, 9, 11, 12, 13, 14, 15, 16, 24, 25, 26} {
		if !strings.HasPrefix(formulaRow[idx], "'") {
			t.Fatalf("csv formula cell %d was not neutralized: %#v", idx, formulaRow)
		}
	}
	if formulaRow[17] != "ordinary, quoted" {
		t.Fatalf("ordinary quoted CSV value changed: %#v", formulaRow)
	}
	allEvents, err := svc.usage.securityAccessEvents(SecurityReportOptions{From: time.Now().UTC().Add(-365 * 24 * time.Hour), To: time.Now().UTC().Add(time.Hour), Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range allEvents {
		if event.RequestID == "req_old_security" {
			t.Fatalf("old security event was not purged from store: %#v", event)
		}
	}
	for _, forbidden := range []string{"invalid-secret-token", testToken, "token_sha256", "provider-key", "messages"} {
		if strings.Contains(reportRR.Body.String(), forbidden) {
			t.Fatalf("security report leaked %q: %s", forbidden, reportRR.Body.String())
		}
	}
}

func TestModelsEndpointIncludesCompatibilityModelsField(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()

	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["data"].([]any); !ok {
		t.Fatalf("missing OpenAI data field: %#v", body)
	}
	for _, forbidden := range []string{"version", "commit", "build_date", "go_version", "goos", "goarch"} {
		if _, ok := body[forbidden]; ok {
			t.Fatalf("/v1/models leaked router build field %q: %#v", forbidden, body)
		}
	}
	if models, ok := body["models"].([]any); !ok || len(models) == 0 {
		t.Fatalf("missing compatibility models field: %#v", body)
	} else if first, ok := models[0].(map[string]any); !ok || first["slug"] == "" || first["display_name"] == "" || first["base_instructions"] == "" || first["context_window"] == nil || first["max_context_window"] == nil || first["shell_type"] == "" || first["supported_in_api"] != true {
		t.Fatalf("missing model compatibility fields: %#v", body)
	}
	for _, want := range []string{"Metrum AI Router model group", "Use Metrum AI Router as the model gateway.", "Default Metrum AI Router service tier"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("models response missing canonical product text %q: %s", want, rr.Body.String())
		}
	}
}

func TestCodexModelsEndpointAuthenticatesFiltersAndMapsSafeCatalog(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Models["big-coder"] = ModelGroup{
		Strategy: "static",
		Contract: &ModelGroupContract{
			DisplayName:        "Coding workspace",
			CallerVisibleNotes: "Deployment-defined coding group.",
		},
		Targets: []Target{{
			Provider:        "mock",
			Model:           "private-upstream-model",
			Dialect:         "openai-responses",
			ContextTokens:   200000,
			InputModalities: []string{"text", "image"},
			ToolSupport:     ToolSupport{OpenAIResponses: []string{"function", "local_shell", "apply_patch"}},
			Reasoning: ReasoningSupport{
				Supported:         true,
				Mode:              reasoningModeOptIn,
				Control:           reasoningControlEffortEnum,
				SupportsSummaries: true,
				DefaultOn:         true,
			},
			Weight: 99,
		}},
	}
	cfg.Models["private-group"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "private-model", ContextTokens: 8192}}}
	cfg.Models["plain"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "plain-model", ContextTokens: 8192}}}
	cfg.Models["tool-only"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "mock",
		Model:           "tool-only-model",
		ToolOnly:        true,
		DefaultThinking: map[string]any{"type": "enabled", "budget_tokens": 512},
	}}}
	cfg.Callers[0].Allow = []string{"big-coder", "plain", "tool-only"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	unauthorized := httptest.NewRequest(http.MethodGet, "/v1/codex/models.json", nil)
	unauthorizedRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(unauthorizedRR, unauthorized)
	if unauthorizedRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorizedRR.Code, unauthorizedRR.Body.String())
	}
	invalid := httptest.NewRequest(http.MethodGet, "/v1/codex/models.json", nil)
	invalid.Header.Set("Authorization", "Bearer invalid-caller-token")
	invalidRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(invalidRR, invalid)
	if invalidRR.Code != http.StatusUnauthorized || strings.Contains(invalidRR.Body.String(), "big-coder") {
		t.Fatalf("invalid caller status=%d body=%s", invalidRR.Code, invalidRR.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/codex/models.json", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	// This local mirror deliberately includes the Codex fields this endpoint
	// promises, so accidental field-name/type drift fails deterministically.
	var catalog struct {
		Models []struct {
			Slug                          string `json:"slug"`
			DisplayName                   string `json:"display_name"`
			Description                   string `json:"description"`
			ContextWindow                 int    `json:"context_window"`
			MaxContextWindow              int    `json:"max_context_window"`
			EffectiveContextWindowPercent int    `json:"effective_context_window_percent"`
			SupportedReasoningLevels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			DefaultReasoningLevel       string         `json:"default_reasoning_level"`
			SupportsReasoningSummaries  bool           `json:"supports_reasoning_summaries"`
			SupportsParallelToolCalls   bool           `json:"supports_parallel_tool_calls"`
			ExperimentalSupportedTools  []string       `json:"experimental_supported_tools"`
			SupportsImageDetailOriginal bool           `json:"supports_image_detail_original"`
			InputModalities             []string       `json:"input_modalities"`
			TruncationPolicy            map[string]any `json:"truncation_policy"`
			ApplyPatchToolType          string         `json:"apply_patch_tool_type"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("catalog models=%#v, want exactly caller-allowed Responses groups", catalog.Models)
	}
	model := catalog.Models[0]
	if model.Slug != "big-coder" || model.DisplayName != "Coding workspace" || model.Description != "Deployment-defined coding group." {
		t.Fatalf("safe identity mapping=%#v", model)
	}
	if model.MaxContextWindow != 200000 || model.ContextWindow != 190000 || model.EffectiveContextWindowPercent != 95 {
		t.Fatalf("context mapping=%#v", model)
	}
	if len(model.SupportedReasoningLevels) != 3 || model.DefaultReasoningLevel != "medium" || !model.SupportsReasoningSummaries {
		t.Fatalf("reasoning mapping=%#v", model)
	}
	if !model.SupportsParallelToolCalls || len(model.ExperimentalSupportedTools) == 0 || !model.SupportsImageDetailOriginal || !stringSliceContains(model.InputModalities, "image") {
		t.Fatalf("tool/modality mapping=%#v", model)
	}
	if model.TruncationPolicy["mode"] != "tokens" || model.ApplyPatchToolType != "freeform" {
		t.Fatalf("static Codex mapping=%#v", model)
	}
	if model.DefaultReasoningLevel == "" || !model.SupportsReasoningSummaries {
		t.Fatalf("active reasoning metadata must remain advertised: %#v", model)
	}
	var rawCatalog struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rawCatalog); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"plain", "tool-only"} {
		var nonReasoning map[string]any
		for _, rawModel := range rawCatalog.Models {
			if rawModel["slug"] == slug {
				nonReasoning = rawModel
				break
			}
		}
		if nonReasoning != nil {
			t.Fatalf("non-Responses Codex model %q was advertised: %#v", slug, nonReasoning)
		}
	}
	for _, forbidden := range []string{"provider-key", testToken, cfg.Callers[0].TokenSHA256, cfg.Provider["mock"].BaseURL, "mock", "private-upstream-model", "private-model", "private-group", "weight", "dialect", "targets"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("Codex catalog leaked %q: %s", forbidden, rr.Body.String())
		}
	}

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer "+testToken)
	modelsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(modelsRR, modelsReq)
	if modelsRR.Code != http.StatusOK || !strings.Contains(modelsRR.Body.String(), `"data"`) {
		t.Fatalf("/v1/models compatibility changed: status=%d body=%s", modelsRR.Code, modelsRR.Body.String())
	}
}

func TestCodexCatalogMatchesResponsesEligibilityAcrossDialects(t *testing.T) {
	var responsesCalls, chatCalls, anthropicCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"id": "resp_catalog", "object": "response", "status": "completed", "model": "native-model", "output": []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "native ok"}}}}})
		case "/v1/chat/completions":
			chatCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"id": "chat_catalog", "model": "chat-model", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "chat ok"}, "finish_reason": "stop"}}})
		case "/v1/messages":
			anthropicCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"id": "msg_catalog", "type": "message", "role": "assistant", "content": []map[string]any{{"type": "text", "text": "anthropic ok"}}, "stop_reason": "end_turn"})
		default:
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider = map[string]ProviderConfig{
		"responses": {BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"},
		"chat":      {BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
		"anthropic": {BaseURL: upstream.URL, Dialect: "anthropic", APIKey: "provider-key"},
	}
	responsesTarget := Target{Provider: "responses", Model: "native-model", ContextTokens: 200000, InputModalities: []string{"text", "image"}, ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}, Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true, DefaultOn: true}}
	chatOnlyTarget := Target{Provider: "chat", Model: "chat-only-model", ContextTokens: 120000, InputModalities: []string{"text", "image"}, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum, SupportsSummaries: true, DefaultOn: true}}
	bridgedTarget := chatOnlyTarget
	bridgedTarget.Model = "bridged-chat-model"
	bridgedTarget.ResponsesToChat = ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, Images: true, Reasoning: true, ValidationStatus: "passed"}
	cfg.Models = map[string]ModelGroup{
		"native-responses": {Strategy: "static", Targets: []Target{responsesTarget}},
		"chat-only":        {Strategy: "static", Targets: []Target{chatOnlyTarget}},
		"bridged-chat":     {Strategy: "static", Targets: []Target{bridgedTarget}},
		"anthropic-only":   {Strategy: "static", Targets: []Target{{Provider: "anthropic", Model: "anthropic-model", ContextTokens: 100000, ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}}}}},
	}
	cfg.Server.DefaultModelGroup = "native-responses"
	cfg.Callers[0].Allow = []string{"native-responses", "chat-only", "bridged-chat", "anthropic-only"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	catalogReq := httptest.NewRequest(http.MethodGet, "/v1/codex/models.json", nil)
	catalogReq.Header.Set("Authorization", "Bearer "+testToken)
	catalogRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(catalogRR, catalogReq)
	if catalogRR.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogRR.Code, catalogRR.Body.String())
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(catalogRR.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, model := range catalog.Models {
		byID[stringValue(model["id"])] = model
	}
	if len(byID) != 2 || byID["native-responses"] == nil || byID["bridged-chat"] == nil || byID["chat-only"] != nil || byID["anthropic-only"] != nil {
		t.Fatalf("Codex catalog must include only Responses-eligible groups: %#v", byID)
	}
	for _, id := range []string{"native-responses", "bridged-chat"} {
		model := byID[id]
		if model["supports_parallel_tool_calls"] != true || model["supports_image_detail_original"] != true || len(model["supported_reasoning_levels"].([]any)) == 0 {
			t.Fatalf("Responses-eligible Codex capabilities missing for %s: %#v", id, model)
		}
	}

	request := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr
	}
	if rr := request("/v1/responses", `{"model":"native-responses","input":"catalog routing"}`); rr.Code != http.StatusOK {
		t.Fatalf("native Responses routing status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request("/v1/responses", `{"model":"chat-only","input":"catalog routing"}`); rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "no-eligible-target") {
		t.Fatalf("unbridged Chat target Responses routing status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request("/v1/responses", `{"model":"bridged-chat","input":"catalog routing"}`); rr.Code != http.StatusOK {
		t.Fatalf("bridged Chat Responses routing status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request("/v1/chat/completions", `{"model":"chat-only","messages":[{"role":"user","content":"catalog routing"}]}`); rr.Code != http.StatusOK {
		t.Fatalf("Chat routing status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request("/v1/messages", `{"model":"anthropic-only","max_tokens":16,"messages":[{"role":"user","content":"catalog routing"}]}`); rr.Code != http.StatusOK {
		t.Fatalf("Anthropic routing status=%d body=%s", rr.Code, rr.Body.String())
	}
	if responsesCalls.Load() != 1 || chatCalls.Load() != 2 || anthropicCalls.Load() != 1 {
		t.Fatalf("upstream calls responses=%d chat=%d anthropic=%d", responsesCalls.Load(), chatCalls.Load(), anthropicCalls.Load())
	}
}

func TestCodexCatalogAdvertisesImagesOnlyForEligibleResponsesPath(t *testing.T) {
	var upstreamModels []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		upstreamModels = append(upstreamModels, stringValue(body["model"]))
		switch r.URL.Path {
		case "/v1/responses":
			writeJSON(w, http.StatusOK, map[string]any{"id": "native-image", "object": "response", "status": "completed", "model": body["model"], "output_text": "ok"})
		case "/v1/chat/completions":
			writeJSON(w, http.StatusOK, map[string]any{"id": "bridged-image", "model": body["model"], "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}})
		default:
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider = map[string]ProviderConfig{
		"responses": {BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"},
		"chat":      {BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
	}
	nativeImage := Target{Provider: "responses", Model: "native-image", InputModalities: []string{"text", "image"}}
	bridgeImage := Target{Provider: "chat", Model: "bridge-image", InputModalities: []string{"text", "image"}, ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, Images: true, ValidationStatus: "passed"}}
	bridgeTextOnly := bridgeImage
	bridgeTextOnly.Model = "bridge-text-only"
	bridgeTextOnly.ResponsesToChat.Images = false
	nativeImageBlocked := nativeImage
	nativeImageBlocked.Model = "native-image-blocked"
	nativeImageBlocked.RequestShapeSupport.UnsupportedRequestFeatures = []string{"image"}
	cfg.Models = map[string]ModelGroup{
		"native-image":         {Strategy: "static", Targets: []Target{nativeImage}},
		"bridge-image":         {Strategy: "static", Targets: []Target{bridgeImage}},
		"bridge-text-only":     {Strategy: "static", Targets: []Target{bridgeTextOnly}},
		"native-image-blocked": {Strategy: "static", Targets: []Target{nativeImageBlocked}},
	}
	cfg.Server.DefaultModelGroup = "native-image"
	cfg.Callers[0].Allow = []string{"native-image", "bridge-image", "bridge-text-only", "native-image-blocked"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	catalogReq := httptest.NewRequest(http.MethodGet, "/v1/codex/models.json", nil)
	catalogReq.Header.Set("Authorization", "Bearer "+testToken)
	catalogRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(catalogRR, catalogReq)
	if catalogRR.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogRR.Code, catalogRR.Body.String())
	}
	var catalog struct {
		Models []struct {
			ID                          string   `json:"id"`
			InputModalities             []string `json:"input_modalities"`
			SupportsImageDetailOriginal bool     `json:"supports_image_detail_original"`
		} `json:"models"`
	}
	if err := json.Unmarshal(catalogRR.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		modalities []string
		supports   bool
	}{}
	for _, model := range catalog.Models {
		byID[model.ID] = struct {
			modalities []string
			supports   bool
		}{model.InputModalities, model.SupportsImageDetailOriginal}
	}
	for _, name := range []string{"native-image", "bridge-image"} {
		model := byID[name]
		if !stringSliceContains(model.modalities, "image") || !model.supports {
			t.Fatalf("eligible Responses image path was not advertised for %s: %#v", name, model)
		}
	}
	for _, name := range []string{"bridge-text-only", "native-image-blocked"} {
		model := byID[name]
		if stringSliceContains(model.modalities, "image") || model.supports {
			t.Fatalf("image-ineligible Responses path was advertised for %s: %#v", name, model)
		}
	}

	request := func(model string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+model+`","input":[{"role":"user","content":[{"type":"input_text","text":"catalog image"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr
	}
	for _, name := range []string{"native-image", "bridge-image"} {
		if rr := request(name); rr.Code != http.StatusOK {
			t.Fatalf("eligible image route %s status=%d body=%s", name, rr.Code, rr.Body.String())
		}
	}
	for _, name := range []string{"bridge-text-only", "native-image-blocked"} {
		if rr := request(name); rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "no-eligible-target") {
			t.Fatalf("ineligible image route %s status=%d body=%s", name, rr.Code, rr.Body.String())
		}
	}
	if got, want := upstreamModels, []string{"native-image", "bridge-image"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("image routes used wrong target identity: got=%#v want=%#v", got, want)
	}
}

func TestCodexModelsEndpointInstalledCLIFetchThenRunSmoke(t *testing.T) {
	if os.Getenv("RUN_CODEX_CATALOG_SMOKE") != "1" {
		t.Skip("set RUN_CODEX_CATALOG_SMOKE=1 to run the installed Codex CLI smoke")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("installed Codex CLI is unavailable")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("upstream path=%s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "resp_codex_catalog_smoke",
			"object": "response",
			"status": "completed",
			"model":  "catalog-smoke-model",
			"output": []map[string]any{{
				"type":    "message",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": "router codex ok"}},
			}},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 3, "total_tokens": 4},
		})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models = map[string]ModelGroup{"catalog-smoke": {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "catalog-smoke-model", Dialect: "openai-responses", ContextTokens: 1000000, ToolSupport: ToolSupport{OpenAIResponses: []string{"function", "tool_choice", "local_shell", "apply_patch"}}}}}}
	cfg.Server.DefaultModelGroup = "catalog-smoke"
	cfg.Callers[0].Allow = []string{"catalog-smoke"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	routerServer := httptest.NewServer(svc.Handler())
	defer routerServer.Close()

	fetchReq, err := http.NewRequest(http.MethodGet, routerServer.URL+"/v1/codex/models.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	fetchReq.Header.Set("Authorization", "Bearer "+testToken)
	fetchResp, err := http.DefaultClient.Do(fetchReq)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := io.ReadAll(fetchResp.Body)
	fetchResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if fetchResp.StatusCode != http.StatusOK {
		t.Fatalf("catalog fetch status=%d", fetchResp.StatusCode)
	}
	catalogPath := filepath.Join(dir, "metrum-models.json")
	if err := os.WriteFile(catalogPath, catalog, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "exec", "--ignore-user-config", "--ephemeral", "--ignore-rules", "--skip-git-repo-check",
		"-c", `model="catalog-smoke"`,
		"-c", `model_provider="metrum-ai-router"`,
		"-c", `model_catalog_json="`+catalogPath+`"`,
		"-c", `model_providers.metrum-ai-router.name="Metrum AI Router"`,
		"-c", `model_providers.metrum-ai-router.base_url="`+routerServer.URL+`/v1"`,
		"-c", `model_providers.metrum-ai-router.env_key="METRUM_ROUTER_KEY"`,
		"-c", `model_providers.metrum-ai-router.wire_api="responses"`,
		"Reply with exactly: router codex ok")
	cmd.Env = append(os.Environ(), "METRUM_ROUTER_KEY="+testToken, "HOME="+dir, "CODEX_HOME="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installed Codex CLI smoke failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "router codex ok") {
		t.Fatalf("installed Codex CLI did not return the expected safe sentinel")
	}
}

func TestVersionAndHealthEndpointsExposeBuildInfo(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	for _, path := range []string{"/version", "/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s json: %v", path, err)
		}
		for _, key := range []string{"version", "commit", "build_date"} {
			if body[key] == "" || body[key] == nil {
				t.Fatalf("%s missing %s: %#v", path, key, body)
			}
		}
		if path == "/version" {
			for _, key := range []string{"go_version", "goos", "goarch"} {
				if body[key] == "" || body[key] == nil {
					t.Fatalf("%s missing %s: %#v", path, key, body)
				}
			}
		} else if body["ok"] != true {
			t.Fatalf("%s ok field=%#v body=%#v", path, body["ok"], body)
		}
	}
}

func TestReadyzDoesNotLeakValidationDetail(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	readyOK := httptest.NewRecorder()
	svc.Handler().ServeHTTP(readyOK, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyOK.Code != http.StatusOK {
		t.Fatalf("healthy /readyz status=%d body=%s", readyOK.Code, readyOK.Body.String())
	}

	// Mutate runtime config so Validate() fails with a detailed message that must not leak.
	leakCallerID := "alice"
	leakPath := "/tmp/secret-config-path.yaml"
	svc.cfg.Models["default"] = ModelGroup{
		Strategy: "script",
		Script:   leakPath,
		Targets:  []Target{{Provider: "mock", Model: "mock-model"}},
	}
	svc.cfg.Callers[0].Allow = []string{"missing-group-for-" + leakCallerID}

	validateErr := svc.cfg.Validate()
	if validateErr == nil {
		t.Fatal("expected Validate() to fail after config mutation")
	}
	detail := validateErr.Error()
	if !strings.Contains(detail, leakCallerID) {
		t.Fatalf("Validate() error=%q does not include caller id %q; adjust mutation", detail, leakCallerID)
	}
	if !strings.Contains(detail, "missing-group-for-"+leakCallerID) && !strings.Contains(detail, leakPath) {
		// Prefer either the allow-list group detail or script path in the validation text.
		t.Fatalf("Validate() error=%q missing expected detail markers", detail)
	}

	ready := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status=%d body=%s, want 503", ready.Code, ready.Body.String())
	}
	var readyBody map[string]any
	if err := json.Unmarshal(ready.Body.Bytes(), &readyBody); err != nil {
		t.Fatalf("/readyz json: %v body=%s", err, ready.Body.String())
	}
	if readyBody["ok"] != false {
		t.Fatalf("/readyz ok=%#v, want false", readyBody["ok"])
	}
	if readyBody["error"] != "not-ready" {
		t.Fatalf("/readyz error=%#v, want not-ready", readyBody["error"])
	}
	raw := ready.Body.String()
	if strings.Contains(raw, detail) {
		t.Fatalf("/readyz leaked Validate() detail %q in body=%s", detail, raw)
	}
	if strings.Contains(raw, leakCallerID) {
		t.Fatalf("/readyz leaked caller id %q in body=%s", leakCallerID, raw)
	}
	if strings.Contains(raw, leakPath) {
		t.Fatalf("/readyz leaked path %q in body=%s", leakPath, raw)
	}
	if strings.Contains(raw, "missing-group-for-") {
		t.Fatalf("/readyz leaked allow-list detail in body=%s", raw)
	}
	if strings.Count(raw, `"error"`) != 1 {
		t.Fatalf("/readyz has duplicate error fields in body=%s", raw)
	}
	for _, key := range []string{"version", "commit", "build_date"} {
		if readyBody[key] == "" || readyBody[key] == nil {
			t.Fatalf("/readyz missing %s: %#v", key, readyBody)
		}
	}

	health := httptest.NewRecorder()
	svc.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("/healthz status=%d body=%s, want 200 after /readyz failure", health.Code, health.Body.String())
	}
	var healthBody map[string]any
	if err := json.Unmarshal(health.Body.Bytes(), &healthBody); err != nil {
		t.Fatalf("/healthz json: %v", err)
	}
	if healthBody["ok"] != true {
		t.Fatalf("/healthz ok=%#v, want true", healthBody["ok"])
	}
	if _, hasError := healthBody["error"]; hasError {
		t.Fatalf("/healthz unexpectedly included error: %#v", healthBody)
	}
}

func TestEmbeddedDocsRootRedirectsToDocs(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	rr := httptest.NewRecorder()

	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "/docs/" {
		t.Fatalf("location=%q", got)
	}
}

func TestEmbeddedDocsAreServedUnderDocs(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/docs/", nil)
	req.Header.Set("Accept", "text/html")
	rr := httptest.NewRecorder()

	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Metrum AI Router") {
		t.Fatalf("root did not serve docs HTML: %s", rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type=%q", ct)
	}
	for _, header := range []string{"X-Metrum-AI-Router-Version", "X-Metrum-AI-Router-Build-Date"} {
		if got := rr.Header().Get(header); got != "" {
			t.Fatalf("public docs exposed build identity header %s=%q", header, got)
		}
	}
	if got := rr.Header().Get("X-Metrum-AI-Router-Commit"); got != "" {
		t.Fatalf("docs response exposed source-control commit header: %q", got)
	}
}

func TestServiceServesSecurityTextBeforeDocsFallback(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Contact: mailto:security@metrum.ai") {
		t.Fatalf("unexpected security.txt body=%q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Policy: https://github.com/metrum-ai/router/blob/main/SECURITY.md") {
		t.Fatalf("security.txt missing Policy URL: %q", rr.Body.String())
	}
}

func TestEmbeddedDocsServeExtensionlessDocusaurusPages(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/docs/solution-brief", nil)
	req.Header.Set("Accept", "text/html")
	rr := httptest.NewRecorder()

	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Solution Brief") {
		t.Fatalf("extensionless doc page did not serve page HTML: %s", rr.Body.String())
	}
}

func TestEmbeddedDocsFallbackDoesNotMaskAPIRoutes(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()

	for _, path := range []string{"/v1/unknown", "/v1", "/metrics/extra"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html")
		rr := httptest.NewRecorder()

		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "Metrum Smart LLM Router Docs") {
			t.Fatalf("%s unexpectedly served docs fallback", path)
		}
	}
}

func TestModelsEndpointMarksAgentToolsSmokeAsToolCapable(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Models["agent-tools-smoke"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "tool-model", Dialect: "openai-responses", ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}}}}
	cfg.Callers[0].Allow = []string{"agent-tools-smoke"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	models := body["models"].([]any)
	first := models[0].(map[string]any)
	if first["supports_parallel_tool_calls"] != true {
		t.Fatalf("agent-tools-smoke not marked tool capable: %#v", first)
	}
	tools := first["experimental_supported_tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("agent-tools-smoke missing supported tools: %#v", first)
	}
}

func TestModelsEndpointReportsVisionInputModalities(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "text-model", InputModalities: []string{"text"}},
		{Provider: "mock", Model: "vision-model", InputModalities: []string{"text", "image", "video"}, OutputModalities: []string{"text"}},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	models := body["models"].([]any)
	first := models[0].(map[string]any)
	if first["supports_image_detail_original"] != true {
		t.Fatalf("vision support not advertised: %#v", first)
	}
	modalities := first["input_modalities"].([]any)
	hasImage := false
	for _, modality := range modalities {
		hasImage = hasImage || modality == "image"
	}
	if !hasImage {
		t.Fatalf("input_modalities missing image: %#v", first)
	}
	for _, modality := range modalities {
		if modality != "text" && modality != "image" {
			t.Fatalf("public input_modalities exposed client-incompatible modality %q: %#v", modality, first)
		}
	}
}

func TestImageRequestsFilterToVisionTargetsAndBypassCache(t *testing.T) {
	var gotModel string
	var gotImageURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel = stringValue(body["model"])
		msgs := body["messages"].([]any)
		content := msgs[0].(map[string]any)["content"].([]any)
		img := content[1].(map[string]any)["image_url"].(map[string]any)
		gotImageURL = stringValue(img["url"])
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_vision",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "Rite Aid"},
			}},
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 4,
				"total_tokens":      104,
				"prompt_tokens_details": map[string]any{
					"image_tokens": 64,
				},
				"cost": 0.00456,
				"cost_details": map[string]any{
					"upstream_inference_prompt_cost":      0.003,
					"upstream_inference_completions_cost": 0.00156,
				},
			},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "text-model", InputModalities: []string{"text"}},
		{
			Provider:                           "mock",
			Model:                              "vision-model",
			InputModalities:                    []string{"text", "image"},
			InputPricePerMillionUSD:            2,
			OutputPricePerMillionUSD:           8,
			ImageInputPricePerMillionTokensUSD: 10,
			ImageInputPricePerImageUSD:         0.001,
		},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model": "default",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Read the receipt."},
				{"type": "image_url", "image_url": {"url": "`+receiptImageURL+`"}}
			]
		}]
	}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	svc.Close()

	if gotModel != "vision-model" || gotImageURL != receiptImageURL {
		t.Fatalf("upstream model/image=%q/%q", gotModel, gotImageURL)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	logText := string(raw)
	for _, want := range []string{
		`"target_model":"vision-model"`,
		`"input_has_image":true`,
		`"input_image_count":1`,
		`"input_image_tokens":64`,
		`"image_input_price_per_million_tokens_usd":10`,
		`"image_input_price_per_image_usd":0.001`,
		`"image_cost_usd":0.00164`,
		`"upstream_reported_total_cost_usd":0.00456`,
		`"cache":"bypass"`,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("vision log missing %s: %s", want, raw)
		}
	}
	if !strings.Contains(logText, `"input_cost_usd":0.000072`) || !strings.Contains(logText, `"output_cost_usd":0.000032`) || !strings.Contains(logText, `"total_cost_usd":0.001744`) {
		t.Fatalf("vision log missing target/image/cache fields: %s", raw)
	}
}

func TestOpenAIResponsesToolPassthroughPreservesToolsAndRawOutput(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		// Native same-dialect streaming: emit provider Responses SSE, not unary JSON.
		w.Header().Set("Content-Type", "text/event-stream")
		item := `{"id":"call_1","type":"function_call","name":"shell","call_id":"call_1","arguments":"{\"cmd\":\"cat > /app/solver.py\"}","status":"completed"}`
		completed := `{"id":"resp_upstream_tool","object":"response","status":"requires_action","model":"gpt-tool","output":[` + item + `],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`
		fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
		fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", completed)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["openai"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["agent-tools-smoke"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "openai", Model: "gpt-tool", ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}}}}
	cfg.Callers[0].Allow = []string{"agent-tools-smoke"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"agent-tools-smoke",
		"input":"write solver",
		"stream":true,
		"tools":[{"type":"function","name":"shell","description":"run shell","parameters":{"type":"object"}}]
	}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", "codex-test")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["model"] != "gpt-tool" {
		t.Fatalf("upstream model=%q", upstreamBody["model"])
	}
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want true for native Responses streaming", upstreamBody["stream"])
	}
	if tools, ok := upstreamBody["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("tools not preserved upstream: %#v", upstreamBody)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type=%q, want event stream; body=%s", ct, rr.Body.String())
	}
	events := parseSSEEvents(t, rr.Body.String())
	itemDone, ok := events["response.output_item.done"]
	if !ok {
		t.Fatalf("tool output SSE missing: %#v", events)
	}
	item := itemDone["item"].(map[string]any)
	if item["type"] != "function_call" || item["call_id"] != "call_1" {
		t.Fatalf("function call not preserved in SSE: %#v", item)
	}
	completed, ok := events["response.completed"]
	if !ok {
		t.Fatalf("completed SSE missing: %#v", events)
	}
	response := completed["response"].(map[string]any)
	if response["id"] != "resp_upstream_tool" || response["status"] != "requires_action" {
		t.Fatalf("raw response not preserved in completed event: %#v", response)
	}
}

func TestOpenAIChatToolPassthroughPreservesToolsAndStreamsToolCalls(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","created":1710000000,"model":"chat-tool","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_weather","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"San Francisco\"}"}}]},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","created":1710000000,"model":"chat-tool","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","created":1710000000,"model":"chat-tool","choices":[],"usage":{"prompt_tokens":17,"completion_tokens":5,"total_tokens":22}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["openai_chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["warp-agent-smoke"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "openai_chat",
		Model:       "chat-tool",
		ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
	}}}
	cfg.Callers[0].Allow = []string{"warp-agent-smoke"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"warp-agent-smoke",
		"stream":true,
		"messages":[{"role":"user","content":"weather"}],
		"tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}],
		"tool_choice":"auto",
		"parallel_tool_calls":true,
		"stream_options":{"include_usage":true}
	}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", "OpenAI/Go 3.15.0")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["model"] != "chat-tool" {
		t.Fatalf("upstream model=%q", upstreamBody["model"])
	}
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want true", upstreamBody["stream"])
	}
	if options, ok := upstreamBody["stream_options"].(map[string]any); !ok || options["include_usage"] != true {
		t.Fatalf("upstream stream_options=%#v, want include_usage", upstreamBody["stream_options"])
	}
	if tools, ok := upstreamBody["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("tools not preserved upstream: %#v", upstreamBody)
	}
	if upstreamBody["tool_choice"] != "auto" || upstreamBody["parallel_tool_calls"] != true {
		t.Fatalf("tool fields not preserved upstream: %#v", upstreamBody)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type=%q, want event stream; body=%s", ct, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"tool_calls"`) ||
		!strings.Contains(body, `"get_weather"`) ||
		!strings.Contains(body, `"finish_reason":"tool_calls"`) ||
		!strings.Contains(body, "data: [DONE]") {
		t.Fatalf("tool call stream not preserved:\n%s", body)
	}
}

func TestCursorOpenAIChatToolsAndImageRequiresSingleCombinedTarget(t *testing.T) {
	var calls atomic.Int64
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_cursor_mixed\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"chat-tools-image\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_cursor_mixed\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"chat-tools-image\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":24000,\"completion_tokens\":1,\"total_tokens\":24001}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := cursorMixedToolsImageConfig(t, upstream.URL, dir, true)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(cursorMixedToolsImagePayload(t, "cursor-coding")))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", "Cursor/1.0")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", calls.Load())
	}
	if upstreamBody["model"] != "chat-tools-image" {
		t.Fatalf("upstream model=%#v body=%#v", upstreamBody["model"], upstreamBody)
	}
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want native streaming upstream", upstreamBody["stream"])
	}
	if tools, ok := upstreamBody["tools"].([]any); !ok || len(tools) != 19 {
		t.Fatalf("tools not preserved upstream: %#v", upstreamBody["tools"])
	}
	messages, ok := upstreamBody["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("messages not preserved upstream: %#v", upstreamBody["messages"])
	}
	userMessage := messages[2].(map[string]any)
	parts := userMessage["content"].([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("image part not preserved upstream: %#v", parts)
	}

	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var shape requestShapeRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	if !shape.Stream || shape.MessageCount != 3 || shape.ToolCount != 19 || shape.ImageCount != 1 || shape.RequestedOutputCapBucket != "omitted" {
		t.Fatalf("unexpected request shape: %#v", shape)
	}
	var candidates []decisionTargetCandidateRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Order("candidate_index ASC").Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 4 {
		t.Fatalf("candidate count=%d: %#v", len(candidates), candidates)
	}
	if candidates[3].Model != "chat-tools-image" || !candidates[3].Eligible || !candidates[3].Selected || !candidates[3].InputImage || !candidates[3].ToolSupport {
		t.Fatalf("combined target was not the only selected eligible target: %#v", candidates[3])
	}
	for i, candidate := range candidates[:3] {
		if candidate.Selected {
			t.Fatalf("incomplete candidate %d was selected: %#v", i, candidate)
		}
	}
	var reasons []decisionTargetFilterReasonRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"input-modality-image", "dialect-tool-passthrough"} {
		if !filterReasonsContain(reasons, "request_shape", want) {
			t.Fatalf("missing filter reason %q: %#v", want, reasons)
		}
	}
	assertCursorMixedTelemetryDoesNotContain(t, svc, usage.RequestID, "RAW_CURSOR_IMAGE", "cursor-private-secret", "provider-key", testToken)
}

func TestCursorOpenAIChatToolsAndImageNoEligiblePersistsDiagnostics(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := cursorMixedToolsImageConfig(t, upstream.URL, dir, false)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(cursorMixedToolsImagePayload(t, "cursor-no-eligible-secret")))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", "Cursor/1.0")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls.Load())
	}
	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.Attempts != 0 || usage.Error != "no-eligible-target" {
		t.Fatalf("usage row=%#v", usage)
	}
	var requestError requestErrorRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&requestError).Error; err != nil {
		t.Fatal(err)
	}
	if requestError.ErrorClass != "no-eligible-target" || requestError.Attempts != 0 {
		t.Fatalf("request error=%#v", requestError)
	}
	var shape requestShapeRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	if !shape.Stream || shape.MessageCount != 3 || shape.ToolCount != 19 || shape.ImageCount != 1 {
		t.Fatalf("unexpected request shape: %#v", shape)
	}
	var features int64
	if err := svc.usage.db.Model(&decisionShapeFeatureRecord{}).Where("request_id = ?", usage.RequestID).Count(&features).Error; err != nil {
		t.Fatal(err)
	}
	if features == 0 {
		t.Fatal("decision shape features were not persisted")
	}
	var candidates []decisionTargetCandidateRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Order("candidate_index ASC").Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidate count=%d: %#v", len(candidates), candidates)
	}
	for _, candidate := range candidates {
		if candidate.Eligible || candidate.Selected {
			t.Fatalf("ineligible request had eligible/selected candidate: %#v", candidate)
		}
	}
	var reasons []decisionTargetFilterReasonRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"input-modality-image", "dialect-tool-passthrough"} {
		if !filterReasonsContain(reasons, "request_shape", want) {
			t.Fatalf("missing filter reason %q: %#v", want, reasons)
		}
	}
	assertCursorMixedTelemetryDoesNotContain(t, svc, usage.RequestID, "RAW_CURSOR_IMAGE", "cursor-no-eligible-secret", "provider-key", testToken)
}

func TestOpenAIChatToolRequestsRequireExplicitToolSupport(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "chat-maybe-tools"}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := &IRRequest{Tools: []map[string]any{{"type": "function"}}, Messages: []IRMessage{{Role: "user", Content: "hi"}}}
	if got := svc.targetsForRequest(nil, cfg.Models["default"].Targets, req, "openai-chat"); len(got) != 0 {
		t.Fatalf("openai-chat target without explicit tool metadata was eligible: %#v", got)
	}
}

func TestAnthropicToolRequestsRequireExplicitToolSupport(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "anthropic", APIKey: "provider-key"}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "messages-no-tool-metadata"},
		{Provider: "mock", Model: "messages-tools", ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}}},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := &IRRequest{Tools: []map[string]any{{"name": "echo", "input_schema": map[string]any{"type": "object"}}}, Messages: []IRMessage{{Role: "user", Content: "hi"}}}
	got := svc.targetsForRequest(nil, cfg.Models["default"].Targets, req, "anthropic")
	if len(got) != 1 || got[0].Model != "messages-tools" {
		t.Fatalf("anthropic tool eligibility=%#v, want only explicit tool target", got)
	}
}

func TestAnthropicMessagesSkipsOpenAITargetsWithoutInboundValidation(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["chat"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai-chat",
		APIKey:  "provider-key",
	}
	cfg.Provider["responses"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai-responses",
		APIKey:  "provider-key",
	}
	cfg.Models["anthropic-inbound"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "chat", Model: "chat-tools", ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}},
		{Provider: "responses", Model: "responses-tools", ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "anthropic-inbound")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"anthropic-inbound","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) {
		t.Fatalf("status=%d body=%s, want no eligible target", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d, want none", upstreamCalls)
	}
	var count int64
	if err := svc.usage.db.Model(&decisionTargetFilterReasonRecord{}).Where("reason = ?", "request-shape-dialect-unsupported").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count < 2 {
		t.Fatalf("dialect filter reason count=%d, want at least 2", count)
	}
}

func TestAnthropicMessagesCanUseNativeAnthropicToolTarget(t *testing.T) {
	var gotPath, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "msg_native",
			"type":        "message",
			"role":        "assistant",
			"model":       gotModel,
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["anthropic_native"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "anthropic", APIKey: "provider-key"}
	cfg.Models["anthropic-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "anthropic_native",
		Model:       "native-messages",
		ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "anthropic-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"anthropic-tools","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"echo","input_schema":{"type":"object","properties":{}}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/messages" || gotModel != "native-messages" {
		t.Fatalf("path/model=%s/%s, want /v1/messages native-messages", gotPath, gotModel)
	}
}

func TestAnthropicMessagesCanUseValidatedTranslatedOpenAITarget(t *testing.T) {
	var gotPath, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_translated",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   gotModel,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "translated ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["anthropic-translated"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "chat",
		Model:    "chat-translated",
		RequestShapeSupport: RequestShapeSupport{
			SupportedInboundDialects: []string{"anthropic"},
			ValidationStatus:         "passed",
		},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "anthropic-translated")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"anthropic-translated","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/chat/completions" || gotModel != "chat-translated" {
		t.Fatalf("path/model=%s/%s, want /v1/chat/completions chat-translated", gotPath, gotModel)
	}
	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.InboundDialect != "anthropic" || usage.TargetDialect != "openai-chat" || usage.TargetModel != "chat-translated" {
		t.Fatalf("usage selected target=%#v", usage)
	}
}

func TestOpenAIChatStructuredOutputPassthrough(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_structured",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   "provider-structured-chat",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": `{"ticket_id":"INC-1234","priority":"high"}`},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["structured_chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["structured-chat-test"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "structured_chat",
		Model:       "provider-structured-chat",
		ToolSupport: ToolSupport{OpenAIChat: []string{"structured_outputs"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "structured-chat-test")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	requestBody := `{
	  "model":"structured-chat-test",
	  "messages":[{"role":"user","content":"Extract INC-1234 high"}],
	  "response_format":{
	    "type":"json_schema",
	    "json_schema":{
	      "name":"ticket_extract",
	      "strict":true,
	      "schema":{
	        "type":"object",
	        "properties":{
	          "ticket_id":{"type":"string"},
	          "priority":{"type":"string","enum":["low","medium","high"]}
	        },
	        "required":["ticket_id","priority"],
	        "additionalProperties":false
	      }
	    }
	  }
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["model"] != "provider-structured-chat" {
		t.Fatalf("upstream model=%#v, want provider model; body=%#v", upstreamBody["model"], upstreamBody)
	}
	expectedRequest := mustJSONMap(t, requestBody)
	assertJSONEquivalent(t, "response_format", upstreamBody["response_format"], expectedRequest["response_format"])
	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	choices := response["choices"].([]any)
	if response["object"] != "chat.completion" || stringValue(response["id"]) == "" || len(choices) != 1 {
		t.Fatalf("not an OpenAI Chat-compatible response: %#v", response)
	}
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != `{"ticket_id":"INC-1234","priority":"high"}` {
		t.Fatalf("structured response content not preserved: %#v", response)
	}
	rawLog, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"ticket_id", "priority", "INC-1234", "ticket_extract"} {
		if strings.Contains(string(rawLog), forbidden) {
			t.Fatalf("request log leaked structured-output schema or prompt text %q: %s", forbidden, rawLog)
		}
	}
}

func TestOpenAIResponsesStructuredOutputPassthrough(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_structured",
			"object":      "response",
			"status":      "completed",
			"model":       "provider-structured-responses",
			"output_text": `{"ticket_id":"INC-1234","priority":"high"}`,
			"output": []map[string]any{{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{{
					"type": "output_text",
					"text": `{"ticket_id":"INC-1234","priority":"high"}`,
				}},
			}},
			"usage": map[string]any{"input_tokens": 13, "output_tokens": 8, "total_tokens": 21},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["structured_responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["structured-responses-test"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "structured_responses",
		Model:       "provider-structured-responses",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"structured_outputs"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "structured-responses-test")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	requestBody := `{
	  "model":"structured-responses-test",
	  "input":"Extract INC-1234 high",
	  "text":{
	    "format":{
	      "type":"json_schema",
	      "name":"ticket_extract",
	      "strict":true,
	      "schema":{
	        "type":"object",
	        "properties":{
	          "ticket_id":{"type":"string"},
	          "priority":{"type":"string","enum":["low","medium","high"]}
	        },
	        "required":["ticket_id","priority"],
	        "additionalProperties":false
	      }
	    }
	  }
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["model"] != "provider-structured-responses" {
		t.Fatalf("upstream model=%#v, want provider model; body=%#v", upstreamBody["model"], upstreamBody)
	}
	expectedRequest := mustJSONMap(t, requestBody)
	assertJSONEquivalent(t, "text.format", upstreamBody["text"], expectedRequest["text"])
	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["object"] != "response" || response["status"] != "completed" {
		t.Fatalf("not a Responses-compatible response: %#v", response)
	}
	usage := response["usage"].(map[string]any)
	if usage["total_tokens"] != float64(21) {
		t.Fatalf("usage not preserved: %#v", usage)
	}
}

func TestStructuredOutputRequestsRequireExplicitSupport(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["structured-chat-test"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "mock",
		Model:       "unsupported-structured-chat",
		ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "structured-chat-test")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"structured-chat-test","messages":[{"role":"user","content":"Extract INC-1234 high"}],"response_format":{"type":"json_schema","json_schema":{"name":"ticket_extract","schema":{"type":"object","properties":{"ticket_id":{"type":"string"}}}}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported structured-output request reached upstream %d times", calls.Load())
	}
	if !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) ||
		!strings.Contains(rr.Body.String(), `structured_outputs`) {
		t.Fatalf("error details missing structured-output requirement: %s", rr.Body.String())
	}
	rawLog, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawLog), `"error":"no-eligible-target"`) ||
		!strings.Contains(string(rawLog), `structured_outputs`) {
		t.Fatalf("request log missing safe scalar error metadata: %s", rawLog)
	}
	for _, forbidden := range []string{"ticket_id", "INC-1234", "ticket_extract"} {
		if strings.Contains(string(rawLog), forbidden) {
			t.Fatalf("request log leaked structured-output schema or prompt text %q: %s", forbidden, rawLog)
		}
	}
}

func TestToolAndStructuredOutputRequestsRequireBothCapabilities(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	req := &IRRequest{
		Messages: []IRMessage{{Role: "user", Content: "Extract INC-1234 high"}},
		Tools:    []map[string]any{{"type": "function"}},
		Raw: map[string]any{
			"response_format": map[string]any{"type": "json_schema"},
		},
	}
	targets := []Target{
		{Provider: "mock", Model: "tools-only", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
		{Provider: "mock", Model: "structured-only", ToolSupport: ToolSupport{OpenAIChat: []string{"structured_outputs"}}},
		{Provider: "mock", Model: "tools-and-structured", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice", "structured_outputs"}}},
		{Provider: "mock", Model: "neither"},
	}
	got := svc.targetsForRequest(nil, targets, req, "openai-chat")
	if len(got) != 1 || got[0].Model != "tools-and-structured" {
		t.Fatalf("eligible targets=%#v, want only tools-and-structured", got)
	}
}

func TestOpenAIChatMaxCompletionTokensSkipsTargetsThatDoNotHonorCaps(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_max_completion_tokens",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   "cap-safe-chat",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "x"},
				"finish_reason": "length",
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 1, "total_tokens": 8},
		})
	}))
	defer upstream.Close()

	honorsMaxTokens := true
	ignoresMaxTokens := false
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["ignored_caps"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["safe_caps"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["chat"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "ignored_caps", Model: "cap-unsafe-chat", InputModalities: []string{"text"}, OutputModalities: []string{"text"}, HonorsMaxTokens: &ignoresMaxTokens},
		{Provider: "safe_caps", Model: "cap-safe-chat", InputModalities: []string{"text"}, OutputModalities: []string{"text"}, HonorsMaxTokens: &honorsMaxTokens},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "chat")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat","max_completion_tokens":1,"messages":[{"role":"user","content":"write a long essay"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstreamBody["model"]; got != "cap-safe-chat" {
		t.Fatalf("upstream model=%#v, want cap-safe-chat; body=%#v", got, upstreamBody)
	}
	if got := upstreamBody["max_tokens"]; got != float64(1) {
		t.Fatalf("upstream max_tokens=%#v, want 1; body=%#v", got, upstreamBody)
	}
	if _, ok := upstreamBody["max_completion_tokens"]; ok {
		t.Fatalf("upstream max_completion_tokens should be normalized for ordinary OpenAI-compatible targets; body=%#v", upstreamBody)
	}
}

func TestOpenAIChatToolPassthroughMaxCompletionTokensFiltersCapUnsafeTargets(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_tool_cap",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   "cap-safe-tool",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]any{{
						"id":   "call_echo",
						"type": "function",
						"function": map[string]any{
							"name":      "echo",
							"arguments": `{"text":"hi"}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 17, "completion_tokens": 1, "total_tokens": 18},
		})
	}))
	defer upstream.Close()

	honorsMaxTokens := true
	ignoresMaxTokens := false
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["ignored_caps"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["safe_caps"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["warp-agent-smoke"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "ignored_caps", Model: "cap-unsafe-tool", HonorsMaxTokens: &ignoresMaxTokens, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
		{Provider: "safe_caps", Model: "cap-safe-tool", HonorsMaxTokens: &honorsMaxTokens, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
	}}
	cfg.Callers[0].Allow = []string{"warp-agent-smoke"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"warp-agent-smoke",
		"stream":true,
		"max_completion_tokens":1,
		"messages":[{"role":"user","content":"echo"}],
		"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object","properties":{"text":{"type":"string"}}}}}],
		"tool_choice":"auto"
	}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstreamBody["model"]; got != "cap-safe-tool" {
		t.Fatalf("upstream model=%#v, want cap-safe-tool; body=%#v", got, upstreamBody)
	}
	if got := upstreamBody["max_tokens"]; got != float64(1) {
		t.Fatalf("upstream max_tokens=%#v, want 1; body=%#v", got, upstreamBody)
	}
	if _, ok := upstreamBody["max_completion_tokens"]; ok {
		t.Fatalf("upstream max_completion_tokens should be normalized for ordinary OpenAI-compatible targets; body=%#v", upstreamBody)
	}
	if tools, ok := upstreamBody["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("tools not preserved upstream: %#v", upstreamBody)
	}
}

func TestAnthropicToolPassthroughPreservesToolsAndStreamsToolUse(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg_tool_1","type":"message","role":"assistant","model":"claude-tool","content":[],"stop_reason":null,"usage":{"input_tokens":13,"output_tokens":0}}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"cat > /app/solver.py\"}"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, "event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":9}}`+"\n\n")
		fmt.Fprint(w, "event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Provider["anthropic_passthrough"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "anthropic", APIKey: "provider-key"}
	cfg.Models["claude-tools-smoke"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "anthropic_passthrough", Model: "claude-tool", ToolSupport: ToolSupport{AnthropicMessages: []string{"client_tools"}}}}}
	cfg.Callers[0].Allow = []string{"claude-tools-smoke"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-tools-smoke",
		"max_tokens":256,
		"stream":true,
		"messages":[{"role":"user","content":"write solver"}],
		"tools":[{"name":"Bash","description":"run shell","input_schema":{"type":"object"}}]
	}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", "claude-code-test")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["model"] != "claude-tool" {
		t.Fatalf("upstream model=%q", upstreamBody["model"])
	}
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream=%#v, want true", upstreamBody["stream"])
	}
	if upstreamBody["max_tokens"] != float64(256) {
		t.Fatalf("upstream max_tokens=%#v, want 256", upstreamBody["max_tokens"])
	}
	if tools, ok := upstreamBody["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("tools not preserved upstream: %#v", upstreamBody)
	}
	events := parseSSEEvents(t, rr.Body.String())
	start, ok := events["content_block_start"]
	if !ok {
		t.Fatalf("content block start missing: %#v", events)
	}
	block := start["content_block"].(map[string]any)
	if block["type"] != "tool_use" || block["name"] != "Bash" || block["id"] != "toolu_1" {
		t.Fatalf("tool_use block not preserved: %#v", block)
	}
	delta, ok := events["content_block_delta"]
	if !ok {
		t.Fatalf("content block delta missing: %#v", events)
	}
	inputDelta := delta["delta"].(map[string]any)
	if inputDelta["type"] != "input_json_delta" {
		t.Fatalf("tool input delta not emitted: %#v", inputDelta)
	}
	messageDelta := events["message_delta"]
	stop := messageDelta["delta"].(map[string]any)
	if stop["stop_reason"] != "tool_use" {
		t.Fatalf("stop reason not preserved: %#v", stop)
	}
}

func TestToolRequestsBypassCache(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_tool_cache",
			"object":      "response",
			"status":      "completed",
			"model":       "gpt-tool",
			"output_text": "ok",
			"output": []map[string]any{{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{{
					"type": "output_text",
					"text": "ok",
				}},
			}},
			"usage": map[string]any{"input_tokens": 3, "output_tokens": 1, "total_tokens": 4},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Cache.Enabled = true
	cfg.Provider["openai"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["agent-tools-smoke"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "openai", Model: "gpt-tool", ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}}}}
	cfg.Callers[0].Allow = []string{"agent-tools-smoke"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"agent-tools-smoke","input":"write file","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("tool requests should bypass cache; upstream calls=%d", calls.Load())
	}
}

func TestOpenAIChatStructuredOutputRequiresMatchingTargetSupport(t *testing.T) {
	var gotModel string
	var gotResponseFormat map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		gotResponseFormat, _ = body["response_format"].(map[string]any)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chat_structured",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": `{"ticket_id":"INC-1234","priority":"high"}`},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 9, "completion_tokens": 7, "total_tokens": 16},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["structured-chat"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "chat-plain", ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}},
		{Provider: "mock", Model: "chat-structured", ToolSupport: ToolSupport{OpenAIChat: []string{"structured_outputs"}}},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "structured-chat")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{
		"model":"structured-chat",
		"messages":[{"role":"user","content":"Extract the ticket id and priority from INC-1234 high"}],
		"response_format":{"type":"json_schema","json_schema":{"name":"ticket_extract","strict":true,"schema":{"type":"object","properties":{"ticket_id":{"type":"string"},"priority":{"type":"string","enum":["low","medium","high"]}},"required":["ticket_id","priority"],"additionalProperties":false}}}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "chat-structured" {
		t.Fatalf("selected model %q, want structured target", gotModel)
	}
	if gotResponseFormat["type"] != "json_schema" {
		t.Fatalf("response_format not forwarded: %#v", gotResponseFormat)
	}
	jsonSchema, _ := gotResponseFormat["json_schema"].(map[string]any)
	if jsonSchema["name"] != "ticket_extract" || jsonSchema["strict"] != true {
		t.Fatalf("schema payload not preserved: %#v", gotResponseFormat)
	}
}

func TestOpenAIResponsesStructuredOutputRequiresMatchingTargetSupport(t *testing.T) {
	var gotModel string
	var gotText map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		gotText, _ = body["text"].(map[string]any)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_structured",
			"object":      "response",
			"status":      "completed",
			"model":       gotModel,
			"output_text": `{"ticket_id":"INC-1234","priority":"high"}`,
			"output": []map[string]any{{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{{
					"type": "output_text",
					"text": `{"ticket_id":"INC-1234","priority":"high"}`,
				}},
			}},
			"usage": map[string]any{"input_tokens": 8, "output_tokens": 7, "total_tokens": 15},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["structured-responses"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "responses", Model: "responses-plain", ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}},
		{Provider: "responses", Model: "responses-structured", ToolSupport: ToolSupport{OpenAIResponses: []string{"structured_outputs"}}},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "structured-responses")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{
		"model":"structured-responses",
		"input":"Extract the ticket id and priority from INC-1234 high",
		"text":{"format":{"type":"json_schema","name":"ticket_extract","strict":true,"schema":{"type":"object","properties":{"ticket_id":{"type":"string"},"priority":{"type":"string","enum":["low","medium","high"]}},"required":["ticket_id","priority"],"additionalProperties":false}}}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "responses-structured" {
		t.Fatalf("selected model %q, want structured target", gotModel)
	}
	format, _ := gotText["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "ticket_extract" || format["strict"] != true {
		t.Fatalf("text.format not preserved: %#v", gotText)
	}
}

func TestOpenAIChatToolsAndStructuredOutputRequireBothCapabilities(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chat_tool_structured",
			"choices": []map[string]any{{
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id":       "call_1",
						"type":     "function",
						"function": map[string]any{"name": "pick", "arguments": `{"ticket_id":"INC-1234"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["tool-structured-chat"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "tools-only", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
		{Provider: "mock", Model: "structured-only", ToolSupport: ToolSupport{OpenAIChat: []string{"structured_outputs"}}},
		{Provider: "mock", Model: "tools-and-structured", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice", "structured_outputs"}}},
		{Provider: "mock", Model: "neither"},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "tool-structured-chat")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{
		"model":"tool-structured-chat",
		"messages":[{"role":"user","content":"Pick the ticket"}],
		"tools":[{"type":"function","function":{"name":"pick","parameters":{"type":"object","properties":{"ticket_id":{"type":"string"}}}}}],
		"tool_choice":"auto",
		"response_format":{"type":"json_schema","json_schema":{"name":"ticket_extract","strict":true,"schema":{"type":"object","properties":{"ticket_id":{"type":"string"}},"required":["ticket_id"],"additionalProperties":false}}}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "tools-and-structured" {
		t.Fatalf("selected model %q, want target with tools and structured outputs", gotModel)
	}
}

func TestStructuredOutputNoEligibleTargetReturnsRequirement(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["plain-chat"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "mock",
		Model:       "plain-chat",
		ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "plain-chat")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"plain-chat","messages":[{"role":"user","content":"Extract INC-1234"}],"response_format":{"type":"json_schema","json_schema":{"name":"ticket_extract","strict":true,"schema":{"type":"object","properties":{"ticket_id":{"type":"string"}},"required":["ticket_id"],"additionalProperties":false}}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream was called %d time(s)", calls.Load())
	}
	if !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) ||
		!strings.Contains(rr.Body.String(), `"structured_outputs"`) {
		t.Fatalf("structured output requirement missing from body=%s", rr.Body.String())
	}
}

func TestPlainTextFormatDoesNotRequireStructuredOutputSupport(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_text_format",
			"object":      "response",
			"status":      "completed",
			"model":       gotModel,
			"output_text": "plain text ok",
			"usage":       map[string]any{"input_tokens": 3, "output_tokens": 3, "total_tokens": 6},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["plain-text-format"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "responses",
		Model:       "responses-plain",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "plain-text-format")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"plain-text-format","input":"Reply plainly.","text":{"format":{"type":"text"}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "responses-plain" {
		t.Fatalf("selected model %q, want plain responses target", gotModel)
	}
}

func TestStructuredOutputRequestsBypassCache(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chat_structured_cache",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": `{"ticket_id":"INC-1234"}`},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 4, "total_tokens": 12},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Cache.Enabled = true
	cfg.Models["structured-cache"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "mock",
		Model:       "structured-cache",
		ToolSupport: ToolSupport{OpenAIChat: []string{"structured_outputs"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "structured-cache")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"structured-cache","messages":[{"role":"user","content":"Extract INC-1234"}],"response_format":{"type":"json_schema","json_schema":{"name":"ticket_extract","strict":true,"schema":{"type":"object","properties":{"ticket_id":{"type":"string"}},"required":["ticket_id"],"additionalProperties":false}}}}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("structured output requests should bypass cache; upstream calls=%d", calls.Load())
	}
}

func parseSSEEvents(t *testing.T, body string) map[string]map[string]any {
	t.Helper()
	events := map[string]map[string]any{}
	for _, frame := range strings.Split(body, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		var event string
		var dataLines []string
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if data == "[DONE]" {
					continue
				}
				dataLines = append(dataLines, data)
			}
		}
		if event == "" || len(dataLines) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &payload); err != nil {
			t.Fatalf("invalid JSON payload for SSE event %q: %v\nframe:\n%s", event, err, frame)
		}
		events[event] = payload
	}
	return events
}

func TestCallerAllowListRestrictsModelGroups(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_allow",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "allowed"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["big-coder"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "big-model"}}}
	cfg.Models["high"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "high-model"}}}
	cfg.Callers[0].Allow = []string{"default"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	allowed := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	allowed.Header.Set("Authorization", "Bearer "+testToken)
	allowedRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(allowedRR, allowed)
	if allowedRR.Code != http.StatusOK {
		t.Fatalf("allowed status=%d body=%s", allowedRR.Code, allowedRR.Body.String())
	}

	for _, model := range []string{"big-coder", "high"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s", model, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "model-not-allowed") {
			t.Fatalf("%s missing model-not-allowed: %s", model, rr.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("disallowed model groups should not call upstream, calls=%d", calls.Load())
	}
}

func TestModelsEndpointOnlyListsAllowedModelGroups(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Models["big-coder"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "big-model"}}}
	cfg.Models["high"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "high-model"}}}
	cfg.Callers[0].Allow = []string{"default"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, model := range body.Data {
		got = append(got, model.ID)
	}
	if strings.Join(got, ",") != "default" {
		t.Fatalf("models=%v, want only default", got)
	}
}

func TestCacheHitAcrossDialectsAndTargetIsolation(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_cache",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "cached answer"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": int(n), "total_tokens": int(n) + 1},
		})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	anthropicInbound := RequestShapeSupport{SupportedInboundDialects: []string{"openai-chat", "anthropic"}, ValidationStatus: "passed"}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model", RequestShapeSupport: anthropicInbound}}}
	cfg.Models["other"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "other-model", RequestShapeSupport: anthropicInbound}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
		return rr
	}

	post("/v1/chat/completions", `{"model":"default","temperature":0,"messages":[{"role":"user","content":"same"}]}`)
	post("/v1/messages", `{"model":"default","temperature":0,"messages":[{"role":"user","content":"same"}]}`)
	if calls.Load() != 1 {
		t.Fatalf("expected cross-dialect cache hit, upstream calls=%d", calls.Load())
	}
	post("/v1/messages", `{"model":"other","temperature":0,"messages":[{"role":"user","content":"same"}]}`)
	if calls.Load() != 2 {
		t.Fatalf("expected separate cache entry for different target, upstream calls=%d", calls.Load())
	}
}

func TestCacheHitSanitizesProviderIDAndRawPayload(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":              "provider-secret-response-id",
			"provider_secret": "raw-provider-metadata",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "cached sanitized"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": int(n), "total_tokens": int(n) + 3},
		})
	}))
	defer upstream.Close()
	svc := newTestService(t, upstream.URL, "provider-key")
	defer svc.Close()

	post := func() map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"id":"caller-specific-id","model":"default","temperature":0,"messages":[{"role":"user","content":"same cache prompt"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		raw := rr.Body.String()
		if strings.Contains(raw, "provider-secret-response-id") || strings.Contains(raw, "raw-provider-metadata") {
			t.Fatalf("provider raw data leaked: %s", raw)
		}
		return body
	}

	first := post()
	second := post()
	if calls.Load() != 1 {
		t.Fatalf("expected cache hit, upstream calls=%d", calls.Load())
	}
	if first["id"] == "" || second["id"] == "" || first["id"] == second["id"] {
		t.Fatalf("expected fresh router IDs, first=%q second=%q", first["id"], second["id"])
	}
	if !strings.HasPrefix(first["id"].(string), "resp_") || !strings.HasPrefix(second["id"].(string), "resp_") {
		t.Fatalf("expected router response IDs, first=%q second=%q", first["id"], second["id"])
	}
}

func TestCacheKeyIgnoresRawRequestID(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "provider-id",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "same"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	svc := newTestService(t, upstream.URL, "provider-key")
	defer svc.Close()

	for _, id := range []string{"caller-id-1", "caller-id-2"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"id":"`+id+`","model":"default","temperature":0,"messages":[{"role":"user","content":"raw id ignored"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected second request to hit cache despite different raw id, calls=%d", calls.Load())
	}
}

func TestCacheTTLAndLRUEviction(t *testing.T) {
	resp := &IRResponse{ID: "provider-id", Model: "m", Text: "cached", Usage: Usage{TotalTokens: 1}, Raw: map[string]any{"id": "provider-id"}}
	short := newCache(CacheConfig{Enabled: true, MaxBytes: 1024, DefaultTTL: time.Nanosecond})
	short.Put("a", resp)
	time.Sleep(time.Millisecond)
	if _, ok := short.Get("a"); ok {
		t.Fatal("expected expired cache entry to miss")
	}

	lru := newCache(CacheConfig{Enabled: true, MaxBytes: 150, DefaultTTL: time.Minute})
	lru.Put("a", &IRResponse{Model: "m", Text: strings.Repeat("a", 40)})
	lru.Put("b", &IRResponse{Model: "m", Text: strings.Repeat("b", 40)})
	if _, ok := lru.Get("a"); ok {
		t.Fatal("expected oldest entry to be evicted")
	}
	if _, ok := lru.Get("b"); !ok {
		t.Fatal("expected newest entry to remain")
	}
}

func TestCacheHitDoesNotConsumeLifetimeQuota(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "quota-cache",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "quota cache"},
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5},
		})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Callers[0].Key.LifetimeTokens = 10
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	post := func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","temperature":0,"messages":[{"role":"user","content":"hi"}],"max_tokens":4}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	post()
	post()
	if calls.Load() != 1 {
		t.Fatalf("expected second request from cache, calls=%d", calls.Load())
	}
	usage := svc.quota.Usage(svc.quota.callers["alice"])
	keyUsage := usage["key"].(map[string]any)
	if keyUsage["lifetime_tokens"] != int64(5) {
		t.Fatalf("expected only upstream request to count against lifetime quota: %#v", keyUsage)
	}
}

func TestCacheIsolatesCallersGC1(t *testing.T) {
	// GC-1: identical temperature:0 prompts from different callers must not share cache hits.
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body := "alice-only-body"
		if n > 1 {
			body = "bob-fresh-body"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "cache-caller-iso",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": body},
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 2, "total_tokens": 4},
		})
	}))
	defer upstream.Close()

	const bobToken = "rtr_bob_cache_iso"
	bobSum := sha256.Sum256([]byte(bobToken))
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Cache.Enabled = true
	cfg.Callers = append(cfg.Callers, CallerConfig{
		ID:          "bob",
		User:        "bob",
		Project:     "other-project",
		Environment: "test",
		TokenSHA256: hex.EncodeToString(bobSum[:]),
		TokenID:     "rtr_bob_test",
		Allow:       []string{"default"},
		Rate:        RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
		Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
		Key:         KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
	})
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","temperature":0,"messages":[{"role":"user","content":"shared prompt"}]}`
	post := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr
	}
	aliceRR := post(testToken)
	if aliceRR.Code != http.StatusOK {
		t.Fatalf("alice status=%d body=%s", aliceRR.Code, aliceRR.Body.String())
	}
	bobRR := post(bobToken)
	if bobRR.Code != http.StatusOK {
		t.Fatalf("bob status=%d body=%s", bobRR.Code, bobRR.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("bob should miss alice cache; upstream calls=%d want 2", calls.Load())
	}
	if !strings.Contains(bobRR.Body.String(), "bob-fresh-body") {
		t.Fatalf("bob got shared/wrong body: %s", bobRR.Body.String())
	}
	aliceAgain := post(testToken)
	if aliceAgain.Code != http.StatusOK {
		t.Fatalf("alice replay status=%d body=%s", aliceAgain.Code, aliceAgain.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("alice same-caller replay should hit cache; calls=%d", calls.Load())
	}
}

func TestConcurrencyAdmitBeforeBodyGC9(t *testing.T) {
	// GC-9: concurrent:1 second request is 429 while first is still reading a slow body.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "slow-body",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Rate.Concurrent = 1
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	type gateBody struct {
		started chan struct{}
		release chan struct{}
		once    sync.Once
		payload []byte
		off     int
	}
	gb := &gateBody{
		started: make(chan struct{}),
		release: make(chan struct{}),
		payload: []byte(`{"model":"default","temperature":0,"messages":[{"role":"user","content":"slow"}]}`),
	}
	readFn := func(p []byte) (int, error) {
		gb.once.Do(func() { close(gb.started) })
		<-gb.release
		if gb.off >= len(gb.payload) {
			return 0, io.EOF
		}
		n := copy(p, gb.payload[gb.off:])
		gb.off += n
		return n, nil
	}
	firstDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(readerFunc(readFn)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		firstDone <- rr.Code
	}()
	select {
	case <-gb.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached body read after admit")
	}

	second := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"second"}]}`))
	second.Header.Set("Authorization", "Bearer "+testToken)
	secondRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(secondRR, second)
	if secondRR.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d body=%s, want 429 before first body finishes", secondRR.Code, secondRR.Body.String())
	}
	close(gb.release)
	select {
	case code := <-firstDone:
		if code != http.StatusOK {
			t.Fatalf("first status=%d, want 200 after body released", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not finish")
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestUnauthenticatedDoesNotAdmitGC10(t *testing.T) {
	// GC-10: unauthorized fails before quota admit / body buffering.
	cfg := testConfig(t, "http://127.0.0.1:9", "provider-key", t.TempDir())
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	huge := strings.NewReader(strings.Repeat("x", 1<<20))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", huge)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s, want 401", rr.Code, rr.Body.String())
	}
	if len(svc.quota.callers) == 0 {
		t.Fatal("expected callers map")
	}
	alice := svc.quota.callers["alice"]
	if alice.inFlight != 0 {
		t.Fatalf("unauthenticated request counted in-flight: %d", alice.inFlight)
	}
}

func TestDailyQuotaAdmissionReservesRequestedMaxTokens(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Quota.Day.Tokens = 20
	cfg.Callers[0].Quota.Month.Tokens = 1000000
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "quota-exhausted") {
		t.Fatalf("body=%s, want quota-exhausted", rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls.Load())
	}
}

func TestMonthlyQuotaAdmissionReservesRequestedMaxOutputTokens(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Callers[0].Quota.Day.Tokens = 1000000
	cfg.Callers[0].Quota.Month.Tokens = 20
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"default","input":"hi","max_output_tokens":100}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "quota-exhausted") {
		t.Fatalf("body=%s, want quota-exhausted", rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls.Load())
	}
}

func TestLifetimeAdmissionReservesRequestedMaxCompletionTokens(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Key.LifetimeTokens = 20
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":100}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "key-exhausted") {
		t.Fatalf("body=%s, want key-exhausted", rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls.Load())
	}
}

func TestAdmissionCountsToolAndStructuredSchemas(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		dialect string
		body    string
	}{
		{
			name:    "chat tools and response_format",
			path:    "/v1/chat/completions",
			dialect: "openai-chat",
			body:    `{"model":"default","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"payload":{"type":"string","description":"` + strings.Repeat("schema ", 80) + `"}}}}}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object","properties":{"payload":{"type":"string","description":"` + strings.Repeat("format ", 80) + `"}}}}}}`,
		},
		{
			name:    "responses tools and text format",
			path:    "/v1/responses",
			dialect: "openai-responses",
			body:    `{"model":"default","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"payload":{"type":"string","description":"` + strings.Repeat("schema ", 80) + `"}}}}],"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object","properties":{"payload":{"type":"string","description":"` + strings.Repeat("format ", 80) + `"}}}}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
			}))
			defer upstream.Close()

			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: tc.dialect, APIKey: "provider-key"}
			cfg.Callers[0].Rate.TPM = 200
			cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "schema-model", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "structured_outputs"}, OpenAIResponses: []string{"function", "structured_outputs"}}}}}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "tpm-exceeded") {
				t.Fatalf("body=%s, want tpm-exceeded", rr.Body.String())
			}
			if calls.Load() != 0 {
				t.Fatalf("upstream calls=%d, want 0", calls.Load())
			}
		})
	}
}

func TestConcurrentReservationsPreventAggregateTokenOvershoot(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "held",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "held"},
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5},
		})
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Rate.Concurrent = 4
	cfg.Callers[0].Quota.Day.Tokens = 50
	cfg.Callers[0].Quota.Month.Tokens = 1000000
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	firstDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":40}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		firstDone <- rr.Code
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach upstream")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi again"}],"max_tokens":40}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	close(release)
	if code := <-firstDone; code != http.StatusOK {
		t.Fatalf("first status=%d", code)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", calls.Load())
	}
}

func TestConcurrentLifetimeReservationRejectionDoesNotDisableKey(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Key.LifetimeTokens = 100
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	caller := svc.quota.callers["alice"]

	ad := svc.quota.Admit(caller, 0)
	if !ad.OK {
		t.Fatalf("admit failed: %#v", ad)
	}
	defer svc.quota.Release(caller)
	resAd := svc.quota.ReserveTokens(caller, 100)
	if !resAd.OK {
		t.Fatalf("initial reserve failed: %#v", resAd)
	}
	rejected := svc.quota.ReserveTokens(caller, 1)
	if rejected.OK || rejected.Status != http.StatusForbidden || rejected.Reason != "key-exhausted" {
		t.Fatalf("second reserve=%#v, want key-exhausted rejection", rejected)
	}
	if _, _, err := svc.quota.RecordTokens(caller, resAd.Reservation, Usage{TotalTokens: 5}); err != nil {
		t.Fatal(err)
	}
	again := svc.quota.ReserveTokens(caller, 10)
	if !again.OK {
		t.Fatalf("key was disabled by reservation-only exhaustion: %#v", again)
	}
	svc.quota.ReleaseReservation(caller, again.Reservation)
}

func TestFailureReleasesTokenReservation(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "temporary", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Callers[0].Quota.Day.Tokens = 50
	cfg.Callers[0].Quota.Month.Tokens = 1000000
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":40}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		return rr.Code
	}
	if code := post(); code != http.StatusBadGateway {
		t.Fatalf("first status=%d", code)
	}
	if code := post(); code != http.StatusBadGateway {
		t.Fatalf("second status=%d", code)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want 2", calls.Load())
	}
}

func TestLifetimeKeyExhaustionReturns403AndPersists(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_quota",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "quota"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 9, "total_tokens": 10},
		})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Callers[0].Key.LifetimeTokens = 10
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.2")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", rr.Code, rr.Body.String())
	}
	svc.Close()

	svc2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"again"}]}`))
	req2.Header.Set("Authorization", "Bearer "+testToken)
	rr2 := httptest.NewRecorder()
	svc2.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Fatalf("second status=%d body=%s", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "key-exhausted") {
		t.Fatalf("missing key-exhausted: %s", rr2.Body.String())
	}
}

func TestCountTokensEndpoint(t *testing.T) {
	for _, path := range []string{"/v1/messages/count_tokens", "/anthropic/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			upstream := httptest.NewServer(http.NotFoundHandler())
			defer upstream.Close()
			svc := newTestService(t, upstream.URL, "provider-key")
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"count these tokens"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var body map[string]int
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["input_tokens"] <= 0 {
				t.Fatalf("bad token estimate: %#v", body)
			}
		})
	}
}

func TestCountTokensRejectsUnauthorizedModelsBeforeUsage(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	enabled := true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &enabled)
	cfg.Models["allowed"] = cfg.Models["default"]
	cfg.Callers[0].Allow = []string{"allowed"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, model := range []string{"default", "fake-model-with-secret-like-name"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"count these tokens"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "model-not-allowed") {
			t.Fatalf("model %s status=%d body=%s", model, rr.Code, rr.Body.String())
		}
	}

	var count int64
	if err := svc.usage.db.Model(&usageRecord{}).Where("requested_model IN ?", []string{"default", "fake-model-with-secret-like-name"}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected count_tokens persisted requested model rows=%d", count)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"allowed","messages":[{"role":"user","content":"count these tokens"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("authorized status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestQuotaStateRejectsRawJSONAfterIntegrityMigration(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	path := filepath.Join(dir, "state.json")
	qs, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	ad := qs.Admit(caller, 0)
	if !ad.OK {
		t.Fatalf("admit failed: %#v", ad)
	}
	if _, _, err := qs.RecordTokens(caller, nil, Usage{TotalTokens: 10}); err != nil {
		t.Fatal(err)
	}
	qs.Release(caller)
	if err := qs.Close(); err != nil {
		t.Fatal(err)
	}

	raw := `{"callers":{"alice":{"day_start":"2026-01-01T00:00:00Z","month_start":"2026-01-01T00:00:00Z","lifetime_tokens":0,"disabled":false}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newQuotaStore(path, cfg); err == nil || !errors.Is(err, errStateIntegrity) {
		t.Fatalf("tampered raw quota state err=%v, want integrity failure", err)
	}
}

func TestQuotaStateExplicitUnsignedMigration(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	path := filepath.Join(dir, "state.json")
	raw := `{"callers":{"alice":{"day_start":"2026-01-01T00:00:00Z","month_start":"2026-01-01T00:00:00Z","lifetime_tokens":7,"disabled":false}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METRUM_AI_ROUTER_ALLOW_UNSIGNED_STATE_MIGRATION", "1")
	qs, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := qs.state.Callers["alice"].LifetimeTokens; got != 7 {
		t.Fatalf("migrated lifetime_tokens=%d, want 7", got)
	}
	if err := qs.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METRUM_AI_ROUTER_ALLOW_UNSIGNED_STATE_MIGRATION", "")
	if _, err := newQuotaStore(path, cfg); err != nil {
		t.Fatalf("signed migrated state rejected: %v", err)
	}
}

func TestQuotaSignedStateSurvivesCallerConfigChanges(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	path := filepath.Join(dir, "state.json")
	qs, err := newQuotaStore(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	caller := qs.callers["alice"]
	ad := qs.Admit(caller, 0)
	if !ad.OK {
		t.Fatalf("admit failed: %#v", ad)
	}
	if _, _, err := qs.RecordTokens(caller, nil, Usage{TotalTokens: 11}); err != nil {
		t.Fatal(err)
	}
	qs.Release(caller)
	if err := qs.Close(); err != nil {
		t.Fatal(err)
	}

	rotatedTokenHash := sha256.Sum256([]byte("rotated-alice-token"))
	bobTokenHash := sha256.Sum256([]byte("bob-token"))
	cfg2 := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	alice := cfg2.Callers[0]
	alice.TokenSHA256 = hex.EncodeToString(rotatedTokenHash[:])
	alice.TokenID = "rtr_alice_rotated"
	bob := alice
	bob.ID = "bob"
	bob.User = "bob"
	bob.OwnerUser = "bob"
	bob.TokenSHA256 = hex.EncodeToString(bobTokenHash[:])
	bob.TokenID = "rtr_bob_test"
	cfg2.Callers = []CallerConfig{bob, alice}

	qs2, err := newQuotaStore(path, cfg2)
	if err != nil {
		t.Fatalf("signed quota state rejected after caller config change: %v", err)
	}
	if got := qs2.state.Callers["alice"].LifetimeTokens; got != 11 {
		t.Fatalf("alice lifetime_tokens=%d, want 11", got)
	}
	if qs2.state.Callers["bob"] == nil {
		t.Fatalf("new caller state not initialized: %#v", qs2.state.Callers)
	}
}

func TestUsageAndLogsIncludeCallerMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_meta",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "metadata"},
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5},
		})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	group := cfg.Models["default"]
	group.Targets[0].InputPricePerMillionUSD = 2.5
	group.Targets[0].OutputPricePerMillionUSD = 7.5
	group.Targets[0].PricingSource = "https://example.test/pricing"
	group.Targets[0].PricingUpdatedAt = "2026-06-17"
	cfg.Models["default"] = group
	cfg.Server.ClientIP.TrustedProxyCIDRs = []string{"192.0.2.0/24"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.2")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	usageReq := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	usageReq.Header.Set("Authorization", "Bearer "+testToken)
	usageRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(usageRR, usageReq)
	if usageRR.Code != http.StatusOK {
		t.Fatalf("usage status=%d body=%s", usageRR.Code, usageRR.Body.String())
	}
	var usage map[string]any
	if err := json.Unmarshal(usageRR.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	if usage["caller_user"] != "alice" || usage["caller_project"] != "metrum-insights" || usage["caller_environment"] != "test" {
		t.Fatalf("usage metadata missing: %#v", usage)
	}
	svc.Close()

	raw, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"caller_user":"alice"`) || !strings.Contains(string(raw), `"caller_project":"metrum-insights"`) || !strings.Contains(string(raw), `"caller_environment":"test"`) {
		t.Fatalf("log metadata missing: %s", raw)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var rec logRecord
	for _, line := range lines {
		var candidate logRecord
		if err := json.Unmarshal([]byte(line), &candidate); err != nil {
			t.Fatal(err)
		}
		if candidate.TargetModel == "mock-model" {
			rec = candidate
			break
		}
	}
	if rec.RequestID == "" {
		t.Fatalf("chat completion record not found: %s", raw)
	}
	if rec.UpstreamMS == nil || *rec.UpstreamMS <= 0 {
		t.Fatalf("upstream duration missing: %#v", rec.UpstreamMS)
	}
	if rec.DownstreamMS == nil || *rec.DownstreamMS <= 0 {
		t.Fatalf("downstream duration missing: %#v", rec.DownstreamMS)
	}
	if rec.UpstreamOutputTPS == nil || rec.DownstreamOutputTPS == nil {
		t.Fatalf("throughput missing: upstream=%#v downstream=%#v", rec.UpstreamOutputTPS, rec.DownstreamOutputTPS)
	}
	if !rec.CacheEnabled || rec.CacheMaxBytes <= 0 {
		t.Fatalf("cache snapshot missing: enabled=%v max=%d", rec.CacheEnabled, rec.CacheMaxBytes)
	}
	if rec.CallerIP != "203.0.113.10" {
		t.Fatalf("caller ip = %q", rec.CallerIP)
	}
	if rec.InputPricePerMillionUSD != 2.5 || rec.OutputPricePerMillionUSD != 7.5 ||
		rec.InputCostUSD != 0.000005 || rec.OutputCostUSD != 0.0000225 || rec.TotalCostUSD != 0.0000275 ||
		rec.PricingSource != "https://example.test/pricing" || rec.PricingUpdatedAt != "2026-06-17" {
		t.Fatalf("cost metadata missing from log record: %#v", rec)
	}
}

func TestMetricsEndpointRequiresMetricsAdminAndExportsGlobalLabels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_metrics",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "metrics"},
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 6, "total_tokens": 10},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	bobToken := "rtr_bob_test_token"
	bobSum := sha256.Sum256([]byte(bobToken))
	adminToken := "rtr_metrics_admin_test_token"
	adminSum := sha256.Sum256([]byte(adminToken))
	cfg.Callers = append(cfg.Callers,
		CallerConfig{
			ID:          "bob",
			User:        "bob",
			Project:     "openfang-daily-reports",
			Environment: "test",
			TokenSHA256: hex.EncodeToString(bobSum[:]),
			TokenID:     "rtr_bob_test",
			Allow:       []string{"default"},
			Rate:        RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
		},
		CallerConfig{
			ID:           "metrics-admin",
			User:         "ops",
			Project:      "observability",
			Environment:  "test",
			TokenSHA256:  hex.EncodeToString(adminSum[:]),
			TokenID:      "rtr_metrics_admin_test",
			Allow:        []string{"default"},
			MetricsAdmin: true,
			Rate:         RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
		},
	)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	unauth := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	unauthRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(unauthRR, unauth)
	if unauthRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauth metrics status=%d body=%s", unauthRR.Code, unauthRR.Body.String())
	}

	for _, token := range []string{testToken, bobToken} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}

	for _, token := range []string{testToken, bobToken} {
		metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		metricsReq.Header.Set("Authorization", "Bearer "+token)
		metricsRR := httptest.NewRecorder()
		svc.Handler().ServeHTTP(metricsRR, metricsReq)
		if metricsRR.Code != http.StatusForbidden {
			t.Fatalf("non-admin metrics status=%d body=%s", metricsRR.Code, metricsRR.Body.String())
		}
		body := metricsRR.Body.String()
		if !strings.Contains(body, "metrics-forbidden") {
			t.Fatalf("non-admin metrics missing metrics-forbidden: %s", body)
		}
		if strings.Contains(body, "caller_user") || strings.Contains(body, "rtr_") || strings.Contains(body, "metrum_ai_router_") {
			t.Fatalf("non-admin metrics leaked metrics data: %s", body)
		}
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsReq.Header.Set("Authorization", "Bearer "+adminToken)
	metricsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(metricsRR, metricsReq)
	if metricsRR.Code != http.StatusOK {
		t.Fatalf("admin metrics status=%d body=%s", metricsRR.Code, metricsRR.Body.String())
	}
	body := metricsRR.Body.String()
	for _, want := range []string{
		`metrum_ai_router_requests_total`,
		`caller_id="alice"`,
		`caller_user="alice"`,
		`caller_project="metrum-insights"`,
		`caller_environment="test"`,
		`token_id="rtr_alice_test"`,
		`caller_id="bob"`,
		`caller_user="bob"`,
		`caller_project="openfang-daily-reports"`,
		`token_id="rtr_bob_test"`,
		`model_group="default"`,
		`target_provider="mock"`,
		`target_model="mock-model"`,
		`metrum_ai_router_tokens_total`,
		`metrum_ai_router_cache_bypass_total`,
		`metrum_ai_router_cache_entries`,
		`metrum_ai_router_upstream_output_tokens_per_second_sum`,
		`metrum_ai_router_downstream_output_tokens_per_second_sum`,
		`metrum_ai_router_build_info`,
		`version="`,
		`build_date="`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestRejectedModelMetricsUseBoundedLabel(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	adminToken := "rtr_metrics_admin_cardinality"
	adminSum := sha256.Sum256([]byte(adminToken))
	cfg.Callers = append(cfg.Callers, CallerConfig{
		ID:           "metrics-admin",
		User:         "ops",
		Project:      "observability",
		Environment:  "test",
		TokenSHA256:  hex.EncodeToString(adminSum[:]),
		TokenID:      "rtr_metrics_admin_cardinality",
		Allow:        []string{"default"},
		MetricsAdmin: true,
		Rate:         RateConfig{RPM: 1000, TPM: 100000, Concurrent: 4},
	})
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for i := 0; i < 25; i++ {
		model := fmt.Sprintf("not-allowed-%d-%s", i, strings.Repeat("x", 120))
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsReq.Header.Set("Authorization", "Bearer "+adminToken)
	metricsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(metricsRR, metricsReq)
	if metricsRR.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", metricsRR.Code, metricsRR.Body.String())
	}
	body := metricsRR.Body.String()
	if got := strings.Count(body, `model_group="rejected_model"`); got == 0 {
		t.Fatalf("rejected model label count=%d body=%s", got, body)
	}
	if strings.Contains(body, "not-allowed-") || strings.Contains(body, strings.Repeat("x", 60)) {
		t.Fatalf("metrics leaked rejected model names: %s", body)
	}
}

func TestCasbinAuthorizationForMetricsAndReports(t *testing.T) {
	hash := mustBcryptHash(t, "yell-yell-yum")
	t.Setenv("SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST", hash)
	dir := t.TempDir()
	policyFile := filepath.Join(dir, "authz-policy.csv")
	if err := os.WriteFile(policyFile, []byte(strings.Join([]string{
		"# caller policy loaded from file",
		"g, caller:alice, metrics_admin, metrum-insights/test",
		"p, metrics_admin, metrum-insights/test, metrics, read",
		"g, basic:reports, reports_admin, local/test",
		"p, reports_admin, local/test, admin:reports, read|export|drilldown",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminAuth.Basic = AdminBasicAuthConfig{
		Enabled:           true,
		AllowInsecureHTTP: true,
		Users: []AdminBasicAuthUser{{
			Username:        "reports",
			PasswordHashEnv: "SMART_ROUTER_ADMIN_PASSWORD_HASH_TEST",
			Subject:         "basic:reports",
			Domain:          "local/test",
		}},
	}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, PolicyFile: policyFile}
	cfg.Server.AdminReports = AdminReportsConfig{Enabled: true, DefaultSince: "24h", MaxRange: "31d", MaxRows: 100}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsReq.Header.Set("Authorization", "Bearer "+testToken)
	metricsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(metricsRR, metricsReq)
	if metricsRR.Code != http.StatusOK {
		t.Fatalf("policy metrics status=%d body=%s", metricsRR.Code, metricsRR.Body.String())
	}
	if !strings.Contains(metricsRR.Body.String(), "metrum_ai_router_build_info") {
		t.Fatalf("policy metrics missing metrics output: %s", metricsRR.Body.String())
	}

	reportsReq := httptest.NewRequest(http.MethodGet, "/admin/reports/api/summary?since=24h", nil)
	reportsReq.SetBasicAuth("reports", "yell-yell-yum")
	reportsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(reportsRR, reportsReq)
	if reportsRR.Code != http.StatusOK {
		t.Fatalf("reports status=%d body=%s", reportsRR.Code, reportsRR.Body.String())
	}

	ordinaryReports := httptest.NewRequest(http.MethodGet, "/admin/reports/api/summary?since=24h", nil)
	ordinaryReports.Header.Set("Authorization", "Bearer "+testToken)
	ordinaryReportsRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(ordinaryReportsRR, ordinaryReports)
	if ordinaryReportsRR.Code != http.StatusForbidden || !strings.Contains(ordinaryReportsRR.Body.String(), "reports-forbidden") {
		t.Fatalf("caller report status=%d body=%s", ordinaryReportsRR.Code, ordinaryReportsRR.Body.String())
	}
}

func TestCasbinAuthorizationRejectsMalformedPolicyFile(t *testing.T) {
	dir := t.TempDir()
	policyFile := filepath.Join(dir, "authz-policy.csv")
	if err := os.WriteFile(policyFile, []byte("p, reports_admin, local/test, admin:reports\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{Enabled: true, PolicyFile: policyFile}
	svc, err := New(cfg)
	if err == nil {
		svc.Close()
		t.Fatal("expected malformed policy file to fail service startup")
	}
	if !strings.Contains(err.Error(), `p lines require subject, domain, object, action`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnthropicMaxTokensForwardedToAnthropicUpstream(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "msg_max_tokens",
			"type":        "message",
			"role":        "assistant",
			"model":       "anthropic-vision",
			"stop_reason": "max_tokens",
			"content":     []map[string]any{{"type": "text", "text": "x"}},
			"usage":       map[string]any{"input_tokens": 7, "output_tokens": 1},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["anthropic_vision"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "anthropic", APIKey: "provider-key"}
	cfg.Models["vision"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:         "anthropic_vision",
		Model:            "anthropic-vision",
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "vision")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"vision","max_tokens":1,"messages":[{"role":"user","content":"write a long essay"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstreamBody["max_tokens"]; got != float64(1) {
		t.Fatalf("upstream max_tokens=%#v, want 1; body=%#v", got, upstreamBody)
	}
}

func TestAnthropicMaxTokensForwardedToOpenAIChatVisionUpstream(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_max_tokens",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   "chat-vision",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "x"},
				"finish_reason": "length",
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 1, "total_tokens": 8},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["chat_vision"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["vision"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:         "chat_vision",
		Model:            "chat-vision",
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
		RequestShapeSupport: RequestShapeSupport{
			SupportedInboundDialects: []string{"anthropic"},
			ValidationStatus:         "passed",
		},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "vision")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"vision","max_tokens":1,"messages":[{"role":"user","content":"write a long essay"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstreamBody["max_tokens"]; got != float64(1) {
		t.Fatalf("upstream max_tokens=%#v, want 1; body=%#v", got, upstreamBody)
	}
}

func TestExplicitMaxTokensSkipsTargetsThatDoNotHonorCaps(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_max_tokens",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   "cap-safe-vision",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "x"},
				"finish_reason": "length",
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 1, "total_tokens": 8},
		})
	}))
	defer upstream.Close()

	honorsMaxTokens := true
	ignoresMaxTokens := false
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["ignored_caps"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["safe_caps"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["vision"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "ignored_caps", Model: "cap-unsafe-vision", InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}, HonorsMaxTokens: &ignoresMaxTokens},
		{Provider: "safe_caps", Model: "cap-safe-vision", InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}, HonorsMaxTokens: &honorsMaxTokens, RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"anthropic"}, ValidationStatus: "passed"}},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "vision")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"vision","max_tokens":1,"messages":[{"role":"user","content":"write a long essay"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstreamBody["model"]; got != "cap-safe-vision" {
		t.Fatalf("upstream model=%#v, want cap-safe-vision; body=%#v", got, upstreamBody)
	}
	if got := upstreamBody["max_tokens"]; got != float64(1) {
		t.Fatalf("upstream max_tokens=%#v, want 1; body=%#v", got, upstreamBody)
	}
}

func TestResponsesMaxOutputTokensSkipsTargetsThatDoNotHonorCaps(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_max_output_tokens",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   "cap-safe-vision",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "x"},
				"finish_reason": "length",
			}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 1, "total_tokens": 8},
		})
	}))
	defer upstream.Close()

	honorsMaxTokens := true
	ignoresMaxTokens := false
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["ignored_caps"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["safe_caps"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["vision"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "ignored_caps", Model: "cap-unsafe-vision", InputModalities: []string{"text"}, OutputModalities: []string{"text"}, HonorsMaxTokens: &ignoresMaxTokens},
		{Provider: "safe_caps", Model: "cap-safe-vision", InputModalities: []string{"text"}, OutputModalities: []string{"text"}, HonorsMaxTokens: &honorsMaxTokens, RequestShapeSupport: RequestShapeSupport{SupportedInboundDialects: []string{"openai-responses"}}, ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true}},
	}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "vision")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"vision","max_output_tokens":1,"input":"write a long essay"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := upstreamBody["model"]; got != "cap-safe-vision" {
		t.Fatalf("upstream model=%#v, want cap-safe-vision; body=%#v", got, upstreamBody)
	}
	if got := upstreamBody["max_tokens"]; got != float64(1) {
		t.Fatalf("upstream max_tokens=%#v, want 1; body=%#v", got, upstreamBody)
	}
}

func TestNoEligibleTargetMentionsMaxTokensWhenCapUnsafeTargetsSkipped(t *testing.T) {
	ignoresMaxTokens := false
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Provider["ignored_caps"] = ProviderConfig{BaseURL: "http://127.0.0.1:1/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["vision"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:         "ignored_caps",
		Model:            "cap-unsafe-vision",
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
		HonorsMaxTokens:  &ignoresMaxTokens,
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "vision")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"vision","max_tokens":1,"messages":[{"role":"user","content":"write a long essay"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "max_tokens") {
		t.Fatalf("body=%s, want max_tokens requirement", rr.Body.String())
	}
}

func TestReplicateProviderAdapter(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "pred_1",
			"status": "succeeded",
			"output": []any{"replicate ", "answer"},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["replicate"] = ProviderConfig{BaseURL: upstream.URL, Dialect: "replicate", APIKey: "replicate-key"}
	cfg.Models["replicate"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "replicate", Model: "owner/model-name"}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "replicate")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"replicate","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/models/owner/model-name/predictions" {
		t.Fatalf("unexpected replicate path %s", gotPath)
	}
	if gotAuth != "Bearer replicate-key" {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
	input := gotBody["input"].(map[string]any)
	if !strings.Contains(input["prompt"].(string), "hi") {
		t.Fatalf("prompt not mapped: %#v", input)
	}
	if !strings.Contains(rr.Body.String(), "replicate answer") {
		t.Fatalf("response not mapped: %s", rr.Body.String())
	}
	usage := svc.quota.Usage(svc.quota.callers["alice"])
	keyUsage := usage["key"].(map[string]any)
	if keyUsage["lifetime_tokens"].(int64) <= 0 {
		t.Fatalf("replicate success did not record token usage: %#v", keyUsage)
	}
}

func TestTypeScriptRoutingStrategy(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "script_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "script routed"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "router.ts")
	if err := os.WriteFile(scriptPath, []byte(`
type Target = {
  provider: string;
  model: string;
  tier?: string;
  weight: number;
  keyConfigured: boolean;
};

type RouteContext = {
  text: string;
  targets: Target[];
};

export function route(ctx: RouteContext) {
  const eligible = ctx.targets
    .map((target, index) => ({ target, index }))
    .filter((entry) => entry.target.keyConfigured && entry.target.weight > 0);

  if (eligible.length === 0) {
    return { targetIndex: 0, classLabel: "prompt-size:no-eligible-targets" };
  }

  const preferredTier = ctx.text.length > 8000 ? "heavy" : "cheap";
  const preferred = eligible.find((entry) => entry.target.tier === preferredTier) || eligible[0];

  return {
    targetIndex: preferred.index,
    fallbackIndexes: eligible
      .filter((entry) => entry.index !== preferred.index)
      .map((entry) => entry.index),
    classLabel: "prompt-size:" + preferredTier,
  };
}
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["scripted"] = ModelGroup{
		Strategy: "script",
		Script:   scriptPath,
		Targets: []Target{
			{Provider: "mock", Model: "cheap-model", Tier: "cheap", Weight: 70},
			{Provider: "mock", Model: "heavy-model", Tier: "heavy", Weight: 30},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "scripted")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	dec, err := svc.pick(nil, "scripted", cfg.Models["scripted"], &IRRequest{
		Model:    "scripted",
		Messages: []IRMessage{{Role: "user", Content: "short question"}},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err != nil {
		t.Fatalf("short script pick: %v", err)
	}
	if dec.Target.Model != "cheap-model" {
		t.Fatalf("short script pick selected %q", dec.Target.Model)
	}
	if dec.ClassLabel == nil || *dec.ClassLabel != "prompt-size:cheap" {
		t.Fatalf("short script class label=%v", dec.ClassLabel)
	}
	if len(dec.Fallbacks) == 0 || dec.Fallbacks[0].Model != "heavy-model" {
		t.Fatalf("short script fallbacks=%#v", dec.Fallbacks)
	}

	dec, err = svc.pick(nil, "scripted", cfg.Models["scripted"], &IRRequest{
		Model:    "scripted",
		Messages: []IRMessage{{Role: "user", Content: strings.Repeat("large prompt ", 900)}},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err != nil {
		t.Fatalf("long script pick: %v", err)
	}
	if dec.Target.Model != "heavy-model" {
		t.Fatalf("long script pick selected %q", dec.Target.Model)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"scripted","messages":[{"role":"user","content":"`+strings.Repeat("large prompt ", 900)+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "heavy-model" {
		t.Fatalf("script selected model %q", gotModel)
	}
}

func TestTypeScriptRoutingContextRawIsRedactedForNonToolChat(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "script_pii_raw_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "script redacted raw"},
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 3, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "router.ts")
	if err := os.WriteFile(scriptPath, []byte(`
type RouteContext = {
  request: {
    raw?: unknown;
  };
};

export function route(ctx: RouteContext) {
  const raw = JSON.stringify(ctx.request.raw || {});
  if (raw.includes("jane.doe@example.com")) {
    throw new Error("script raw context leaked original PII");
  }
  if (!raw.includes("[EMAIL_1]")) {
    throw new Error("script raw context missing redacted placeholder: " + raw);
  }
  return { targetIndex: 0, classLabel: "script-pii-redacted" };
}
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["scripted-pii"] = ModelGroup{
		Strategy:  "script",
		Script:    scriptPath,
		PIIFilter: testPIIFilterConfig("redact_only"),
		Targets:   []Target{{Provider: "mock", Model: "script-pii-model"}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "scripted-pii")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"scripted-pii","messages":[{"role":"user","content":"Email jane.doe@example.com"}],"max_tokens":16}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "script-pii-model" {
		t.Fatalf("script selected model %q", gotModel)
	}
}

func TestTypeScriptRoutingCanUseCallerTokenRegex(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "script_key_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "script key routed"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "router.ts")
	if err := os.WriteFile(scriptPath, []byte(`
type Ctx = { caller?: { tokenId: string; user: string; project: string }; targets: Array<{ model: string }> };
export function route(ctx: Ctx) {
  if (/^rtr_alice_/.test(ctx.caller?.tokenId || "") && /^metrum-/.test(ctx.caller?.project || "")) {
    return { targetIndex: 1, classLabel: "key-regex:" + ctx.caller?.user };
  }
  return { targetIndex: 0, classLabel: "key-regex:fallback" };
}
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["keyed"] = ModelGroup{
		Strategy: "script",
		Script:   scriptPath,
		Targets: []Target{
			{Provider: "mock", Model: "default-key-model"},
			{Provider: "mock", Model: "alice-key-model"},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "keyed")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"keyed","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "alice-key-model" {
		t.Fatalf("script selected model %q", gotModel)
	}
}

func TestTypeScriptRequestShapeExampleRoutesBySafeContext(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	scriptPath := filepath.Join(repoRoot, "examples", "typescript-request-shape", "router.ts")

	cfg := testConfig(t, "http://example.invalid", "provider-key", t.TempDir())
	cfg.Models["script-request-shape"] = ModelGroup{
		Strategy: "script",
		Script:   scriptPath,
		Targets: []Target{
			{Provider: "mock", Model: "cheap-model", Tier: "cheap", Weight: 70, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
			{Provider: "mock", Model: "heavy-model", Tier: "heavy", Weight: 20, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum}},
			{Provider: "mock", Model: "tool-model", Tier: "tool", Weight: 10, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
			{Provider: "mock", Model: "vision-model", Tier: "vision", Weight: 5, InputModalities: []string{"text", "image"}, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
			{Provider: "mock", Model: "reasoning-model", Tier: "reasoning", Weight: 5, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}, Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum}},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "script-request-shape")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	short, err := svc.pick(nil, "script-request-shape", cfg.Models["script-request-shape"], &IRRequest{
		Model:    "script-request-shape",
		Messages: []IRMessage{{Role: "user", Content: "short question"}},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err != nil {
		t.Fatalf("short request-shape pick: %v", err)
	}
	if short.Target.Model != "cheap-model" {
		t.Fatalf("short request-shape selected %q", short.Target.Model)
	}
	if short.ClassLabel == nil || *short.ClassLabel != "request-shape:chat" {
		t.Fatalf("short class label=%v", short.ClassLabel)
	}

	long, err := svc.pick(nil, "script-request-shape", cfg.Models["script-request-shape"], &IRRequest{
		Model:    "script-request-shape",
		Messages: []IRMessage{{Role: "user", Content: strings.Repeat("large prompt ", 900)}},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err != nil {
		t.Fatalf("long request-shape pick: %v", err)
	}
	if long.Target.Model != "heavy-model" {
		t.Fatalf("long request-shape selected %q", long.Target.Model)
	}
	if long.ClassLabel == nil || *long.ClassLabel != "request-shape:long-context" {
		t.Fatalf("long class label=%v", long.ClassLabel)
	}

	tools, err := svc.pick(nil, "script-request-shape", cfg.Models["script-request-shape"], &IRRequest{
		Model:    "script-request-shape",
		Messages: []IRMessage{{Role: "user", Content: "use a tool"}},
		Tools: []map[string]any{
			{"type": "function", "function": map[string]any{"name": "lookup"}},
		},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err != nil {
		t.Fatalf("tools request-shape pick: %v", err)
	}
	if tools.Target.Model != "tool-model" {
		t.Fatalf("tools request-shape selected %q", tools.Target.Model)
	}
	if tools.ClassLabel == nil || *tools.ClassLabel != "request-shape:tools" {
		t.Fatalf("tools class label=%v", tools.ClassLabel)
	}

	reasoning, err := svc.pick(nil, "script-request-shape", cfg.Models["script-request-shape"], &IRRequest{
		Model:     "script-request-shape",
		Messages:  []IRMessage{{Role: "user", Content: "think carefully"}},
		Reasoning: ReasoningIntent{Requested: true, Kind: "effort", Effort: "medium", Source: "openai_chat.reasoning_effort"},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err != nil {
		t.Fatalf("reasoning request-shape pick: %v", err)
	}
	if reasoning.Target.Model != "reasoning-model" {
		t.Fatalf("reasoning request-shape selected %q", reasoning.Target.Model)
	}
	if reasoning.ClassLabel == nil || *reasoning.ClassLabel != "request-shape:reasoning" {
		t.Fatalf("reasoning class label=%v", reasoning.ClassLabel)
	}
}

func TestTypeScriptPIIPolicyExampleRoutesSensitiveWithoutLeakingPII(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	scriptPath := filepath.Join(repoRoot, "examples", "typescript-pii-policy", "router.ts")

	cfg := testConfig(t, "http://example.invalid", "provider-key", t.TempDir())
	cfg.Models["pii-aware"] = ModelGroup{
		Strategy: "script",
		Script:   scriptPath,
		Targets: []Target{
			{Provider: "mock", Model: "normal-model", DisplayName: "Normal target", Tier: "normal", Weight: 90},
			{Provider: "mock", Model: "private-model", DisplayName: "Private sensitive target", Tier: "private", Weight: 10},
			{Provider: "mock", Model: "sensitive-fallback-model", DisplayName: "Sensitive fallback target", Tier: "sensitive", Weight: 5},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "pii-aware")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	piiText := "Please summarize the account note for Jane Patient. Email jane.patient@example.com and SSN 123-45-6789 are in the record."
	dec, err := svc.pick(nil, "pii-aware", cfg.Models["pii-aware"], &IRRequest{
		Model:    "pii-aware",
		Messages: []IRMessage{{Role: "user", Content: piiText}},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err != nil {
		t.Fatalf("pii script pick: %v", err)
	}
	if dec.Target.Model != "private-model" {
		t.Fatalf("pii script pick selected %q", dec.Target.Model)
	}
	if len(dec.Fallbacks) != 1 || dec.Fallbacks[0].Model != "sensitive-fallback-model" {
		t.Fatalf("pii script fallbacks=%#v, want only sensitive fallback target", dec.Fallbacks)
	}
	if dec.ClassLabel == nil || *dec.ClassLabel != "pii-detected:sensitive-route" {
		t.Fatalf("pii script class label=%v", dec.ClassLabel)
	}
	rawDecision, err := json.Marshal(dec)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"jane.patient@example.com", "123-45-6789", "Jane Patient"} {
		if strings.Contains(string(rawDecision), forbidden) {
			t.Fatalf("script decision leaked raw pii %q: %s", forbidden, rawDecision)
		}
	}

	dec, err = svc.pick(nil, "pii-aware", cfg.Models["pii-aware"], &IRRequest{
		Model:    "pii-aware",
		Messages: []IRMessage{{Role: "user", Content: "Summarize this public release note in one sentence."}},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err != nil {
		t.Fatalf("non-pii script pick: %v", err)
	}
	if dec.Target.Model != "normal-model" {
		t.Fatalf("non-pii script pick selected %q", dec.Target.Model)
	}
	if len(dec.Fallbacks) != 2 {
		t.Fatalf("non-pii script fallbacks=%#v, want remaining eligible targets", dec.Fallbacks)
	}
	if dec.ClassLabel == nil || *dec.ClassLabel != "pii-detected:none" {
		t.Fatalf("non-pii script class label=%v", dec.ClassLabel)
	}
}

func TestTypeScriptPIIPolicyExampleFailsClosedWithoutSensitiveTarget(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	scriptPath := filepath.Join(repoRoot, "examples", "typescript-pii-policy", "router.ts")

	cfg := testConfig(t, "http://example.invalid", "provider-key", t.TempDir())
	cfg.Models["pii-aware"] = ModelGroup{
		Strategy: "script",
		Script:   scriptPath,
		Targets: []Target{
			{Provider: "mock", Model: "normal-model", DisplayName: "Normal target", Tier: "normal", Weight: 90},
			{Provider: "mock", Model: "public-fallback-model", DisplayName: "Public fallback target", Tier: "normal", Weight: 10},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "pii-aware")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	_, err = svc.pick(nil, "pii-aware", cfg.Models["pii-aware"], &IRRequest{
		Model:    "pii-aware",
		Messages: []IRMessage{{Role: "user", Content: "Contact Jane Patient at jane.patient@example.com."}},
	}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test")
	if err == nil {
		t.Fatal("pii script pick succeeded without a sensitive target")
	}
	var policyErr routingPolicyError
	if !errors.As(err, &policyErr) || policyErr.Err == nil || !strings.Contains(policyErr.Err.Error(), "pii-detected:no-sensitive-target") {
		t.Fatalf("pii script error=%v, want fail-closed no-sensitive-target error", err)
	}
}

func TestTypeScriptRoutingCanUseBundledImportsAndAllowedHTTP(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "script_http_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "script http routed"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer policy-secret" {
			t.Fatalf("missing policy auth header %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"tier": "heavy"})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.ts"), []byte(`
export function chooseTier(text: string) {
  return text.includes("force") ? "heavy" : "cheap";
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "router.ts")
	if err := os.WriteFile(scriptPath, []byte(`
import { chooseTier } from "./policy";

type RouteContext = {
  text: string;
  targets: Array<{ tier?: string; keyConfigured: boolean; weight: number }>;
};

export function route(ctx: RouteContext) {
  const response = router.fetchJSON("`+policy.URL+`/route", {
    method: "POST",
    body: { hint: chooseTier(ctx.text) },
  });
  const tier = response.ok ? response.body.tier : chooseTier(ctx.text);
  const targetIndex = ctx.targets.findIndex((target) =>
    target.keyConfigured && target.weight > 0 && target.tier === tier
  );
  return { targetIndex: targetIndex >= 0 ? targetIndex : 0, classLabel: "external-policy:" + tier };
}
`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["script-http"] = ModelGroup{
		Strategy: "script",
		Script:   scriptPath,
		ScriptHTTP: ScriptHTTPConfig{
			Enabled:          true,
			AllowHosts:       []string{policyURL.Hostname()},
			TimeoutMS:        500,
			MaxResponseBytes: 4096,
			Headers:          map[string]string{"Authorization": "Bearer policy-secret"},
		},
		Targets: []Target{
			{Provider: "mock", Model: "cheap-model", Tier: "cheap", Weight: 50},
			{Provider: "mock", Model: "heavy-model", Tier: "heavy", Weight: 50},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "script-http")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"script-http","messages":[{"role":"user","content":"force external policy"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "heavy-model" {
		t.Fatalf("script selected model %q", gotModel)
	}
}

func TestTypeScriptRoutingRejectsHTTPHostOutsideAllowlist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when script policy fails")
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"tier": "heavy"})
	}))
	defer policy.Close()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "router.ts")
	if err := os.WriteFile(scriptPath, []byte(`
export function route() {
  router.fetchJSON("`+policy.URL+`/route");
  return { targetIndex: 0 };
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["script-http-blocked"] = ModelGroup{
		Strategy: "script",
		Script:   scriptPath,
		ScriptHTTP: ScriptHTTPConfig{
			Enabled:    true,
			AllowHosts: []string{"policy.internal.example"},
			TimeoutMS:  500,
		},
		Targets: []Target{{Provider: "mock", Model: "cheap-model", Weight: 1}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "script-http-blocked")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"script-http-blocked","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "routing-policy-error") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestTypeScriptRoutingRejectsHTTPRedirectOutsideAllowlist(t *testing.T) {
	for _, tt := range []struct {
		name        string
		redirectTo  func(targetURL string) string
		wantBlocked string
	}{
		{
			name:        "loopback IP",
			redirectTo:  func(targetURL string) string { return targetURL + "/secret" },
			wantBlocked: "host 127.0.0.1 is not allowed",
		},
		{
			name:        "public host",
			redirectTo:  func(string) string { return "https://example.com/secret" },
			wantBlocked: "host example.com is not allowed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var redirectedReached atomic.Bool
			redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirectedReached.Store(true)
				writeJSON(w, http.StatusOK, map[string]any{"tier": "heavy"})
			}))
			defer redirected.Close()
			policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tt.redirectTo(redirected.URL), http.StatusFound)
			}))
			defer policy.Close()
			policyURL := testURLWithHostname(t, policy.URL, "localhost")

			dir := t.TempDir()
			scriptPath := filepath.Join(dir, "router.ts")
			if err := os.WriteFile(scriptPath, []byte(`
export function route() {
  router.fetchJSON("`+policyURL+`/route");
  return { targetIndex: 0 };
}
`), 0600); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("upstream should not be called when script policy redirect is blocked")
			}))
			defer upstream.Close()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			cfg.Models["script-http-redirect-blocked"] = ModelGroup{
				Strategy: "script",
				Script:   scriptPath,
				ScriptHTTP: ScriptHTTPConfig{
					Enabled:    true,
					AllowHosts: []string{"localhost"},
					TimeoutMS:  500,
				},
				Targets: []Target{{Provider: "mock", Model: "cheap-model", Weight: 1}},
			}
			cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "script-http-redirect-blocked")
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"script-http-redirect-blocked","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "routing-policy-error") {
				t.Fatalf("unexpected body=%s", rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "/secret") {
				t.Fatalf("redirect error leaked URL path: %s", rr.Body.String())
			}
			if redirectedReached.Load() {
				t.Fatal("redirected server was reached")
			}
		})
	}
}

func TestTypeScriptRoutingAllowsHTTPRedirectToAllowedHost(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "script_http_redirect_allowed",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/route" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 0})
	}))
	defer policy.Close()
	policyURL := testURLWithHostname(t, policy.URL, "localhost")

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "router.ts")
	if err := os.WriteFile(scriptPath, []byte(`
export function route() {
  const response = router.fetchJSON("`+policyURL+`/route");
  return { targetIndex: response.body.targetIndex };
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Models["script-http-redirect-allowed"] = ModelGroup{
		Strategy: "script",
		Script:   scriptPath,
		ScriptHTTP: ScriptHTTPConfig{
			Enabled:    true,
			AllowHosts: []string{"localhost"},
			TimeoutMS:  500,
		},
		Targets: []Target{{Provider: "mock", Model: "cheap-model", Weight: 1}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "script-http-redirect-allowed")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"script-http-redirect-allowed","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "cheap-model" {
		t.Fatalf("selected model %q", gotModel)
	}
}

func TestExternalRoutingPolicyStrategy(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "external_policy_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "external policy routed"},
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5},
		})
	}))
	defer upstream.Close()

	var policyPayload map[string]any
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer policy-secret" {
			t.Fatalf("missing policy auth header %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&policyPayload); err != nil {
			t.Fatal(err)
		}
		context, _ := policyPayload["context"].(map[string]any)
		textChars, _ := context["textChars"].(float64)
		if textChars > 8000 {
			writeJSON(w, http.StatusOK, map[string]any{
				"targetIndex":     1,
				"fallbackIndexes": []int{0},
				"classLabel":      "external-policy:heavy",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 0, "classLabel": "external-policy:cheap"})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["external-policy"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:              policy.URL + "/route",
			AllowHosts:       []string{policyURL.Hostname()},
			TimeoutMS:        500,
			MaxResponseBytes: 4096,
			Headers:          map[string]string{"Authorization": "Bearer policy-secret"},
		},
		Targets: []Target{
			{Provider: "mock", Model: "cheap-model", Tier: "cheap", Weight: 70, InputPricePerMillionUSD: 0.1, OutputPricePerMillionUSD: 0.2, Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum}},
			{Provider: "mock", Model: "heavy-model", Tier: "heavy", Weight: 30, InputPricePerMillionUSD: 1.0, OutputPricePerMillionUSD: 2.0, Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum}},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"external-policy","reasoning_effort":"high","messages":[{"role":"user","content":"`+strings.Repeat("large prompt ", 900)+`"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "heavy-model" {
		t.Fatalf("external policy selected model %q", gotModel)
	}
	if policyPayload["group"] != "external-policy" {
		t.Fatalf("policy payload missing group: %#v", policyPayload)
	}
	targets, _ := policyPayload["targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("policy payload targets=%#v", policyPayload["targets"])
	}
	firstTarget, _ := targets[0].(map[string]any)
	if firstTarget["inputPricePerMillionUsd"] != 0.1 || firstTarget["keyConfigured"] != true {
		t.Fatalf("policy target metadata missing: %#v", firstTarget)
	}
	caller, _ := policyPayload["caller"].(map[string]any)
	if caller["tokenId"] == "" || caller["user"] != "alice" {
		t.Fatalf("policy caller metadata missing: %#v", caller)
	}
	rawPayload, _ := json.Marshal(policyPayload)
	if strings.Contains(string(rawPayload), testToken) || strings.Contains(string(rawPayload), "provider-key") || strings.Contains(string(rawPayload), cfg.Callers[0].TokenSHA256) {
		t.Fatalf("policy payload leaked secret material: %s", rawPayload)
	}
	for _, forbidden := range []string{"large prompt"} {
		if strings.Contains(string(rawPayload), forbidden) {
			t.Fatalf("default policy payload leaked raw request context %q: %s", forbidden, rawPayload)
		}
	}
	if _, ok := policyPayload["request"]; ok {
		t.Fatalf("default policy payload included request: %s", rawPayload)
	}
	if _, ok := policyPayload["text"]; ok {
		t.Fatalf("default policy payload included text: %s", rawPayload)
	}
	context, _ := policyPayload["context"].(map[string]any)
	if context["textChars"].(float64) <= 8000 || context["estimatedTokens"].(float64) <= 0 {
		t.Fatalf("policy context missing safe size signals: %#v", context)
	}
	reasoning, _ := context["reasoning"].(map[string]any)
	if reasoning["requested"] != true || reasoning["kind"] != "effort" || reasoning["effort"] != "high" || reasoning["source"] != "openai_chat_reasoning_effort" {
		t.Fatalf("policy context missing safe reasoning signals: %#v", context)
	}
}

func TestExternalRoutingPolicyTrustedOutcomeDecision(t *testing.T) {
	var selectedModels []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		selectedModels = append(selectedModels, body["model"].(string))
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "outcome_policy",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
		})
	}))
	defer upstream.Close()

	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		raw, ok := payload["request"].(map[string]any)
		if !ok {
			t.Fatal("trusted outcome policy did not receive include_request mirror")
		}
		messages, _ := raw["messages"].([]any)
		message, _ := messages[0].(map[string]any)
		content, _ := message["content"].(string)
		targetIndex := 1 // Strong default for unclassified work.
		label := "outcome-policy:unclassified"
		switch content {
		case "What is 2+2?":
			targetIndex, label = 0, "outcome-policy:arithmetic"
		case "Create a runnable load-testing benchmark for HTTP servers.":
			targetIndex, label = 1, "outcome-policy:benchmark-code"
		case "Show a folder listing.":
			targetIndex, label = 0, "outcome-policy:folder-listing"
		}
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": targetIndex, "classLabel": label})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["outcome-policy"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL: policy.URL + "/route", AllowHosts: []string{policyURL.Hostname()},
			TimeoutMS: 500, MaxResponseBytes: 4096, IncludeRequest: true,
		},
		Targets: []Target{
			{Provider: "mock", Model: "cheap-text", Weight: 50, InputPricePerMillionUSD: 0.1, OutputPricePerMillionUSD: 0.2},
			{Provider: "mock", Model: "strong-code", Weight: 50, InputPricePerMillionUSD: 1.0, OutputPricePerMillionUSD: 2.0},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "outcome-policy")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, prompt := range []string{
		"What is 2+2?",
		"Create a runnable load-testing benchmark for HTTP servers.",
		"Show a folder listing.",
		"Write a poem about a moonlit river.",
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"outcome-policy","messages":[{"role":"user","content":`+strconv.Quote(prompt)+`}],"max_tokens":64}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("prompt=%q status=%d body=%s", prompt, rr.Code, rr.Body.String())
		}
	}
	want := []string{"cheap-text", "strong-code", "cheap-text", "strong-code"}
	if !reflect.DeepEqual(selectedModels, want) {
		t.Fatalf("selected models=%v want=%v", selectedModels, want)
	}
}

func TestOutcomeCalibratedPolicyReferenceEndToEnd(t *testing.T) {
	root := filepath.Join("..", "..")
	script := filepath.Join(root, "examples", "external-routing-policy", "outcome_calibrated_policy.py")
	dataset := filepath.Join(root, "examples", "external-routing-policy", "outcome_coding_dataset.json")
	reviews := filepath.Join(root, "examples", "external-routing-policy", "reviewed_coding_outcomes.jsonl")
	profile := filepath.Join(t.TempDir(), "profile.json")
	patch := filepath.Join(t.TempDir(), "weights.yaml")
	calibrate := exec.Command("python3", script, "calibrate", "--dataset", dataset, "--reviews", reviews, "--out-profile", profile, "--out-yaml", patch)
	if output, err := calibrate.CombinedOutput(); err != nil {
		t.Fatalf("calibrate outcome reference: %v output=%s", err, output)
	}
	if _, err := os.Stat(patch); err != nil {
		t.Fatalf("outcome weight patch missing: %v", err)
	}

	vectors := map[string][]float64{
		"Rename a variable in this Python function and return the code only.":                                   {1, 0, 0},
		"Write a Python function that parses a CSV file and returns validated records with unit tests.":         {0, 1, 0},
		"Create a runnable HTTP server load-testing benchmark with concurrency controls, reporting, and tests.": {0, 0, 1},
		"Rename a local variable in this Python function and return only runnable code.":                        {1, 0, 0},
		"Parse CSV records with validation and include Python unit tests.":                                      {0, 1, 0},
		"Build a runnable concurrent HTTP load-testing benchmark with reports and tests.":                       {0, 0, 1},
		"Explain quantum entanglement in one sentence.":                                                         {-1, 0, 0},
	}
	embeddings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		data := make([]map[string]any, 0, len(body.Input))
		for index, input := range body.Input {
			vector, ok := vectors[input]
			if !ok {
				t.Fatalf("unexpected embedding input %q", input)
			}
			data = append(data, map[string]any{"index": index, "embedding": vector})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}))
	defer embeddings.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var policyStderr bytes.Buffer
	policyProcess := exec.Command("python3", script, "serve", "--profile", profile, "--embedding-base-url", embeddings.URL, "--embedding-model", "fake-embedding-model", "--port", strconv.Itoa(port))
	policyProcess.Stderr = &policyStderr
	if err := policyProcess.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = policyProcess.Process.Kill()
		_ = policyProcess.Wait()
	}()
	policyURL := fmt.Sprintf("http://127.0.0.1:%d/route", port)
	ready := false
	for attempt := 0; attempt < 50; attempt++ {
		response, callErr := http.Post(policyURL, "application/json", strings.NewReader(`{}`))
		if callErr == nil {
			_ = response.Body.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("outcome policy did not start: %s", policyStderr.String())
	}

	var selectedModels []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		model, _ := body["model"].(string)
		selectedModels = append(selectedModels, model)
		writeJSON(w, http.StatusOK, map[string]any{"id": "outcome-demo", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
	}))
	defer upstream.Close()
	parsedPolicyURL, err := url.Parse(policyURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["outcome-coding-demo"] = ModelGroup{Strategy: "external", ExternalPolicy: ExternalPolicyConfig{URL: policyURL, AllowHosts: []string{parsedPolicyURL.Hostname()}, TimeoutMS: 500, MaxResponseBytes: 4096, IncludeRequest: true}, Targets: []Target{{Provider: "mock", Model: "cheap-coder", Weight: 34, InputPricePerMillionUSD: 0.1, OutputPricePerMillionUSD: 0.2}, {Provider: "mock", Model: "medium-coder", Weight: 33, InputPricePerMillionUSD: 0.4, OutputPricePerMillionUSD: 0.8}, {Provider: "mock", Model: "strong-coder", Weight: 33, InputPricePerMillionUSD: 1.2, OutputPricePerMillionUSD: 2.4}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "outcome-coding-demo")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	cases := []struct{ prompt, want, class string }{
		{"Rename a local variable in this Python function and return only runnable code.", "cheap-coder", "simple-coding"},
		{"Parse CSV records with validation and include Python unit tests.", "medium-coder", "medium-coding"},
		{"Build a runnable concurrent HTTP load-testing benchmark with reports and tests.", "strong-coder", "difficult-coding"},
		{"Explain quantum entanglement in one sentence.", "strong-coder", "unclassified"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"outcome-coding-demo","messages":[{"role":"user","content":`+strconv.Quote(tc.prompt)+`}],"max_tokens":256}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("class=%s status=%d body=%s", tc.class, rr.Code, rr.Body.String())
		}
		got := selectedModels[len(selectedModels)-1]
		evidence, _ := json.Marshal(map[string]string{"class": tc.class, "prompt": tc.prompt, "selectedModel": got, "expectedModel": tc.want})
		t.Logf("OUTCOME_DEMO %s", evidence)
	}
	if !reflect.DeepEqual(selectedModels, []string{"cheap-coder", "medium-coder", "strong-coder", "strong-coder"}) {
		t.Fatalf("selected models=%v", selectedModels)
	}
}

func TestExternalRoutingPolicyRequestSummaryDoesNotDoubleCountMirroredText(t *testing.T) {
	req := &IRRequest{
		Model:  "external-policy",
		System: "system",
		Input:  "mirrored user request",
		InputParts: []IRContentPart{
			{Type: "text", Text: "mirrored user request"},
		},
		Messages: []IRMessage{{
			Role:    "user",
			Content: "mirrored user request",
			Parts: []IRContentPart{
				{Type: "text", Text: "mirrored user request"},
			},
		}},
	}

	context := buildRequestSummary(req, "openai-responses")
	wantTextChars := len(req.System) + len("mirrored user request")
	if context.TextChars != wantTextChars {
		t.Fatalf("text chars double-counted mirrored request text: got %d want %d", context.TextChars, wantTextChars)
	}
	if context.InputChars != 0 || context.InputPartCount != 0 {
		t.Fatalf("mirrored input should not be counted when messages are canonical: %#v", context)
	}
	if context.MessageTextChars != len("mirrored user request") || context.MessagePartCount != 1 {
		t.Fatalf("message summary did not use canonical message parts: %#v", context)
	}
}

func TestExternalRoutingPolicySafeDefaultOmitsRawRequestForAllDialects(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		dialect string
		body    string
	}{
		{
			name:    "openai-chat",
			path:    "/v1/chat/completions",
			dialect: "openai-chat",
			body:    `{"model":"external-policy-pii","messages":[{"role":"user","content":"Email jane.doe@example.com"}],"max_tokens":16}`,
		},
		{
			name:    "openai-responses",
			path:    "/v1/responses",
			dialect: "openai-responses",
			body:    `{"model":"external-policy-pii","input":"Email jane.doe@example.com","max_output_tokens":16}`,
		},
		{
			name:    "anthropic",
			path:    "/v1/messages",
			dialect: "anthropic",
			body:    `{"model":"external-policy-pii","messages":[{"role":"user","content":"Email jane.doe@example.com"}],"max_tokens":16}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch tc.dialect {
				case "anthropic":
					writeJSON(w, http.StatusOK, map[string]any{
						"id":          "msg_external_pii",
						"type":        "message",
						"content":     []map[string]any{{"type": "text", "text": "ok"}},
						"stop_reason": "end_turn",
						"usage":       map[string]any{"input_tokens": 4, "output_tokens": 1},
					})
				case "openai-responses":
					writeJSON(w, http.StatusOK, map[string]any{
						"id":          "resp_external_pii",
						"output_text": "ok",
						"usage":       map[string]any{"input_tokens": 4, "output_tokens": 1, "total_tokens": 5},
					})
				default:
					writeJSON(w, http.StatusOK, map[string]any{
						"id": "chat_external_pii",
						"choices": []map[string]any{{
							"message": map[string]any{"role": "assistant", "content": "ok"},
						}},
						"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
					})
				}
			}))
			defer upstream.Close()

			var policyPayload map[string]any
			policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&policyPayload); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(policyPayload)
				if strings.Contains(string(raw), "jane.doe@example.com") {
					t.Fatalf("external policy payload leaked raw request text: %s", raw)
				}
				if strings.Contains(string(raw), "[EMAIL_1]") {
					t.Fatalf("external policy payload included request text by default: %s", raw)
				}
				if _, ok := policyPayload["request"]; ok {
					t.Fatalf("external policy payload included request by default: %s", raw)
				}
				if _, ok := policyPayload["text"]; ok {
					t.Fatalf("external policy payload included text by default: %s", raw)
				}
				writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 0, "classLabel": "external-policy-pii-redacted"})
			}))
			defer policy.Close()
			policyURL, err := url.Parse(policy.URL)
			if err != nil {
				t.Fatal(err)
			}

			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: tc.dialect, APIKey: "provider-key"}
			cfg.Models["external-policy-pii"] = ModelGroup{
				Strategy:  "external",
				PIIFilter: testPIIFilterConfig("redact_only"),
				ExternalPolicy: ExternalPolicyConfig{
					URL:        policy.URL + "/route",
					AllowHosts: []string{policyURL.Hostname()},
					TimeoutMS:  500,
				},
				Targets: []Target{{Provider: "mock", Model: "policy-pii-model"}},
			}
			cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy-pii")
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if policyPayload == nil {
				t.Fatal("external policy was not called")
			}
		})
	}
}

func TestExternalRoutingPolicyIncludeRequestOptInSendsRedactedRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chat_external_pii_opt_in",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
		})
	}))
	defer upstream.Close()

	var policyPayload map[string]any
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&policyPayload); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(policyPayload)
		if strings.Contains(string(raw), "jane.doe@example.com") {
			t.Fatalf("external policy opt-in payload leaked raw content: %s", raw)
		}
		for _, want := range []string{`"request"`, `"text"`, "[EMAIL_1]"} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("external policy opt-in payload missing %q: %s", want, raw)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 0})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["external-policy-pii-opt-in"] = ModelGroup{
		Strategy:  "external",
		PIIFilter: testPIIFilterConfig("redact_only"),
		ExternalPolicy: ExternalPolicyConfig{
			URL:            policy.URL + "/route",
			AllowHosts:     []string{policyURL.Hostname()},
			TimeoutMS:      500,
			IncludeRequest: true,
		},
		Targets: []Target{{Provider: "mock", Model: "policy-pii-model"}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy-pii-opt-in")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"external-policy-pii-opt-in","messages":[{"role":"user","content":"Email jane.doe@example.com"}],"max_tokens":16}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if policyPayload == nil {
		t.Fatal("external policy was not called")
	}
}

func TestExternalRoutingPolicySafeDefaultOmitsImagesAndToolOutputs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chat_external_safe_default",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 1, "total_tokens": 9},
		})
	}))
	defer upstream.Close()

	var policyPayload map[string]any
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&policyPayload); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(policyPayload)
		for _, forbidden := range []string{
			"Read this image",
			"https://example.com/private-receipt.png",
			"data:image/png;base64,RAW_IMAGE_DATA",
			"Customer email jane.doe@example.com",
			"lookup_customer",
			`"request"`,
		} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("external policy safe default leaked %q: %s", forbidden, raw)
			}
		}
		if _, ok := policyPayload["request"]; ok {
			t.Fatalf("external policy safe default included request: %s", raw)
		}
		if _, ok := policyPayload["text"]; ok {
			t.Fatalf("external policy safe default included text: %s", raw)
		}
		context, _ := policyPayload["context"].(map[string]any)
		if context["imageCount"].(float64) != 2 || context["toolCount"].(float64) != 1 || context["hasTools"] != true {
			t.Fatalf("policy context missing safe image/tool counts: %#v", context)
		}
		requirements, _ := policyPayload["requirements"].([]any)
		if !containsAnyString(requirements, "image") || !containsAnyString(requirements, "tools") {
			t.Fatalf("policy requirements missing image/tool markers: %#v", requirements)
		}
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 0})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["external-policy-safe-default"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        policy.URL + "/route",
			AllowHosts: []string{policyURL.Hostname()},
			TimeoutMS:  500,
		},
		Targets: []Target{{Provider: "mock", Model: "safe-default-model", InputModalities: []string{"text", "image"}, ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy-safe-default")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{
	  "model":"external-policy-safe-default",
	  "messages":[
	    {"role":"user","content":[
	      {"type":"text","text":"Read this image"},
	      {"type":"image_url","image_url":{"url":"https://example.com/private-receipt.png"}},
	      {"type":"image_url","image_url":{"url":"data:image/png;base64,RAW_IMAGE_DATA"}}
	    ]},
	    {"role":"tool","tool_call_id":"call_123","content":"Customer email jane.doe@example.com"}
	  ],
	  "tools":[{"type":"function","function":{"name":"lookup_customer","parameters":{"type":"object","properties":{}}}}],
	  "tool_choice":"auto"
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if policyPayload == nil {
		t.Fatal("external policy was not called")
	}
}

func containsAnyString(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestExternalRoutingPolicyRejectsHTTPRedirectOutsideAllowlist(t *testing.T) {
	for _, tt := range []struct {
		name        string
		redirectTo  func(targetURL string) string
		wantBlocked string
	}{
		{
			name:        "loopback IP",
			redirectTo:  func(targetURL string) string { return targetURL + "/secret" },
			wantBlocked: "host 127.0.0.1 is not allowed",
		},
		{
			name:        "public host",
			redirectTo:  func(string) string { return "https://example.com/secret" },
			wantBlocked: "host example.com is not allowed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var redirectedReached atomic.Bool
			redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirectedReached.Store(true)
				writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 0})
			}))
			defer redirected.Close()
			policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tt.redirectTo(redirected.URL), http.StatusFound)
			}))
			defer policy.Close()
			policyURL := testURLWithHostname(t, policy.URL, "localhost")

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("upstream should not be called when external policy redirect is blocked")
			}))
			defer upstream.Close()
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Models["external-policy-redirect-blocked"] = ModelGroup{
				Strategy: "external",
				ExternalPolicy: ExternalPolicyConfig{
					URL:        policyURL + "/route",
					AllowHosts: []string{"localhost"},
					TimeoutMS:  500,
				},
				Targets: []Target{{Provider: "mock", Model: "cheap-model", Weight: 1}},
			}
			cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy-redirect-blocked")
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"external-policy-redirect-blocked","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "routing-policy-error") || !strings.Contains(rr.Body.String(), tt.wantBlocked) {
				t.Fatalf("unexpected body=%s", rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "/secret") {
				t.Fatalf("redirect error leaked URL path: %s", rr.Body.String())
			}
			if redirectedReached.Load() {
				t.Fatal("redirected server was reached")
			}
		})
	}
}

func TestExternalRoutingPolicyAllowsHTTPRedirectToAllowedHost(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "external_policy_redirect_allowed",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/route" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 0})
	}))
	defer policy.Close()
	policyURL := testURLWithHostname(t, policy.URL, "localhost")

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["external-policy-redirect-allowed"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        policyURL + "/route",
			AllowHosts: []string{"localhost"},
			TimeoutMS:  500,
		},
		Targets: []Target{{Provider: "mock", Model: "cheap-model", Weight: 1}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy-redirect-allowed")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"external-policy-redirect-allowed","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "cheap-model" {
		t.Fatalf("selected model %q", gotModel)
	}
}

func TestExternalRoutingPolicyInvalidDecisionFailsClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when external policy returns invalid decision")
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 99})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["external-policy"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        policy.URL + "/route",
			AllowHosts: []string{policyURL.Hostname()},
			TimeoutMS:  500,
		},
		Targets: []Target{{Provider: "mock", Model: "cheap-model", Weight: 1}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"external-policy","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "routing-policy-error") {
		t.Fatalf("missing routing-policy-error: %s", rr.Body.String())
	}
}

func TestModelGroupContractFiltersWeightedTargetsAndRecordsUsage(t *testing.T) {
	var selectedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		selectedModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "chatcmpl_contract",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	enabled := true
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &enabled)
	minScore := 0.9
	cfg.Models["contracted"] = ModelGroup{
		Strategy: "weighted",
		Contract: &ModelGroupContract{
			IntendedWorkloads:  []string{"support-chat"},
			SupportedAPIShapes: []string{"openai_chat"},
			QualityFloor: ContractQualityFloor{
				RequireTags:             []string{"validated"},
				MinEvalQualityScore:     &minScore,
				AllowedValidationStatus: []string{"passed"},
			},
			Reporting: ContractReporting{ExposeWorkloadLabels: true},
		},
		Targets: []Target{
			{Provider: "mock", Model: "low-quality", Weight: 100, Tags: []string{"validated"}, Validation: &TargetValidation{Status: "passed", Workload: "support-chat", ValidatedAt: "2026-06-24", QualityScore: 0.7, PassRate: 1, Harness: "unit"}},
			{Provider: "mock", Model: "validated", Weight: 1, Tags: []string{"validated"}, Validation: &TargetValidation{Status: "passed", Workload: "support-chat", ValidatedAt: "2026-06-24", QualityScore: 0.95, PassRate: 1, Harness: "unit"}},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "contracted")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"contracted","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if selectedModel != "validated" {
		t.Fatalf("selected model %q, want validated", selectedModel)
	}
	var records []usageRecord
	if err := svc.usage.db.Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("usage rows=%d, want 1", len(records))
	}
	row, err := rowFromUsageRecord(records[0])
	if err != nil {
		t.Fatal(err)
	}
	if !row.ContractPresent || row.ContractBucket != "passed" || row.ContractWorkload != "support-chat" {
		t.Fatalf("contract usage metadata missing: %#v", row)
	}
	if row.TargetValidationStatus != "passed" || row.TargetValidationWorkload != "support-chat" || row.TargetValidationAgeBucket == "" {
		t.Fatalf("validation usage metadata missing: %#v", row)
	}
}

func TestModelGroupContractNoEligibleTargetStaysGroupLocal(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Fatal("upstream should not be called when requested group contract has no eligible target")
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["support-chat"] = ModelGroup{
		Strategy: "static",
		Contract: &ModelGroupContract{
			SupportedAPIShapes: []string{"openai_chat"},
			RequiredCaps:       ContractRequiredCapabilities{InputModalities: []string{"text"}},
		},
		Targets: []Target{{Provider: "mock", Model: "support-text", InputModalities: []string{"text"}}},
	}
	cfg.Models["receipt-ocr"] = ModelGroup{
		Strategy: "static",
		Contract: &ModelGroupContract{
			SupportedAPIShapes: []string{"openai_chat"},
			RequiredCaps:       ContractRequiredCapabilities{InputModalities: []string{"image"}},
		},
		Targets: []Target{{Provider: "mock", Model: "ocr-image", InputModalities: []string{"text", "image"}}},
	}
	cfg.Callers[0].Allow = []string{"support-chat"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"support-chat","messages":[{"role":"user","content":[{"type":"text","text":"read it"},{"type":"image_url","image_url":{"url":"https://example.test/receipt.png"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls)
	}
	if strings.Contains(rr.Body.String(), "receipt-ocr") || strings.Contains(rr.Body.String(), "ocr-image") {
		t.Fatalf("no-eligible-target leaked other group details: %s", rr.Body.String())
	}
}

func TestModelGroupContractQualityFloorReasonBuckets(t *testing.T) {
	minScore := 0.9
	contract := &ModelGroupContract{
		SupportedAPIShapes: []string{"openai_chat"},
		QualityFloor: ContractQualityFloor{
			RequireTags:             []string{"validated"},
			MinEvalQualityScore:     &minScore,
			MaxEvalAgeDays:          30,
			AllowedValidationStatus: []string{"passed"},
		},
	}
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name   string
		target Target
		want   string
	}{
		{
			name:   "passes",
			target: Target{Provider: "mock", Model: "ok", Tags: []string{"validated"}, Validation: &TargetValidation{Status: "passed", Workload: "support", ValidatedAt: "2026-06-24", QualityScore: 0.95}},
		},
		{
			name:   "low score",
			target: Target{Provider: "mock", Model: "low", Tags: []string{"validated"}, Validation: &TargetValidation{Status: "passed", Workload: "support", ValidatedAt: "2026-06-24", QualityScore: 0.5}},
			want:   "contract-quality-floor",
		},
		{
			name:   "stale",
			target: Target{Provider: "mock", Model: "stale", Tags: []string{"validated"}, Validation: &TargetValidation{Status: "passed", Workload: "support", ValidatedAt: "2026-04-01", QualityScore: 0.95}},
			want:   "contract-validation-expired",
		},
		{
			name:   "missing validation",
			target: Target{Provider: "mock", Model: "missing", Tags: []string{"validated"}},
			want:   "contract-no-validated-target",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := targetPassesContract(contract, tt.target, "openai-chat", "openai-chat", &IRRequest{}, dynamicStats{}, now)
			if got != tt.want {
				t.Fatalf("reason=%q, want %q", got, tt.want)
			}
		})
	}
}

func testURLWithHostname(t *testing.T, rawURL, hostname string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if port := u.Port(); port != "" {
		u.Host = hostname + ":" + port
	} else {
		u.Host = hostname
	}
	return u.String()
}

func TestExternalRoutingPolicyCanFallbackOnError(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "external_policy_fallback_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "fallback routed"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "policy down", http.StatusServiceUnavailable)
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["external-policy"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        policy.URL + "/route",
			AllowHosts: []string{policyURL.Hostname()},
			TimeoutMS:  500,
			OnError:    "fallback",
		},
		Targets: []Target{
			{Provider: "mock", Model: "fallback-model", Weight: 1},
			{Provider: "mock", Model: "second-model", Weight: 1},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"external-policy","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "fallback-model" {
		t.Fatalf("fallback selected model %q", gotModel)
	}
}

func TestPIIFilterRejectsResponseRestoration(t *testing.T) {
	cfg := testConfig(t, "http://localhost", "provider-key", t.TempDir())
	group := cfg.Models["default"]
	group.PIIFilter = testPIIFilterConfig("redact_and_restore")
	cfg.Models["default"] = group
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "F-011") {
		t.Fatalf("expected F-011, got %v", err)
	}
}

func TestPIIFilterReplacementLimitDoesNotLetRawMirrorStarveNormalizedRequest(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(upstreamBody)
		if strings.Contains(string(raw), "jane.doe@example.com") {
			t.Fatalf("upstream received raw PII after raw mirror consumed budget: %s", raw)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "pii_limit_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "Use [EMAIL_1]."},
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 3, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	filter := testPIIFilterConfig("redact_only")
	filter.MaxReplacementsPerRequest = 1
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{
		Strategy:  "static",
		PIIFilter: filter,
		Targets:   []Target{{Provider: "mock", Model: "mock-model"}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"Email jane.doe@example.com"}],"max_tokens":16}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	msgs := upstreamBody["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].(string)
	if content != "Email [EMAIL_1]" {
		t.Fatalf("upstream content=%q, want normalized placeholder", content)
	}
}

func TestPIIFilterReplacementLimitBlocksBeforeUpstream(t *testing.T) {
	for _, mode := range []string{"redact_only", "fail_on_match"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
			}))
			defer upstream.Close()

			filter := testPIIFilterConfig(mode)
			filter.MaxReplacementsPerRequest = 1
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Models["default"] = ModelGroup{
				Strategy:  "static",
				PIIFilter: filter,
				Targets:   []Target{{Provider: "mock", Model: "mock-model"}},
			}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"Email jane.doe@example.com and jane.alt@example.com"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "pii-filter-blocked") {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if calls.Load() != 0 {
				t.Fatalf("upstream calls=%d, want 0", calls.Load())
			}
		})
	}
}

func TestPIIFilterCacheStoresRedactedResponseNotRestoredPII(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "pii_cache_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "Contact [EMAIL_1]."},
			}},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 5, "total_tokens": 13},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Cache.DefaultTTL = time.Minute
	cfg.Models["default"] = ModelGroup{
		Strategy:  "static",
		PIIFilter: testPIIFilterConfig("redact_only"),
		Targets:   []Target{{Provider: "mock", Model: "mock-model"}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	post := func(email string) string {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","temperature":0,"messages":[{"role":"user","content":"Email `+email+`."}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}
	first := post("alice@example.com")
	second := post("bob@example.com")
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want cache hit on second request", calls.Load())
	}
	if !strings.Contains(first, "[EMAIL_1]") || strings.Contains(first, "bob@example.com") {
		t.Fatalf("first response=%s", first)
	}
	if !strings.Contains(second, "[EMAIL_1]") || strings.Contains(second, "alice@example.com") {
		t.Fatalf("second response=%s", second)
	}
}

func TestContentCaptureResponseStoresRedactedPIIPlaceholders(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "pii_capture_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "Contact [EMAIL_1]."},
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 3, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Cache.DefaultTTL = time.Minute
	cfg.Server.ContentCapture = ContentCaptureConfig{
		Enabled:             true,
		RetentionDays:       7,
		CaptureResponse:     true,
		RedactBeforeStorage: boolPtr(true),
		Encryption:          testContentCaptureEncryption(t),
	}
	cfg.Models["default"] = ModelGroup{
		Strategy:  "static",
		PIIFilter: testPIIFilterConfig("redact_only"),
		Targets:   []Target{{Provider: "mock", Model: "mock-model"}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","temperature":0,"messages":[{"role":"user","content":"Email jane.doe@example.com"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "[EMAIL_1]") {
		t.Fatalf("response did not preserve PII placeholders: %s", rr.Body.String())
	}
	var row contentCaptureRecord
	if err := svc.usage.db.Where("request_id = ? AND scope = ?", rr.Header().Get("X-Request-Id"), contentCaptureScopeResponse).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	plaintext := testContentCapturePlaintext(t, row.ContentText, row.EncryptionNonce, row.EncryptionLocalKeyID)
	if strings.Contains(row.ContentText, "jane.doe@example.com") || strings.Contains(plaintext, "jane.doe@example.com") {
		t.Fatalf("response capture stored restored PII: %s", row.ContentText)
	}
	if !strings.Contains(plaintext, "[EMAIL_1]") {
		t.Fatalf("response capture missing placeholder after decrypt: %s", plaintext)
	}

	cacheReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","temperature":0,"messages":[{"role":"user","content":"Email jane.alt@example.com"}]}`))
	cacheReq.Header.Set("Authorization", "Bearer "+testToken)
	cacheRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(cacheRR, cacheReq)
	if cacheRR.Code != http.StatusOK {
		t.Fatalf("cache status=%d body=%s", cacheRR.Code, cacheRR.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want second response from cache", calls.Load())
	}
	if !strings.Contains(cacheRR.Body.String(), "[EMAIL_1]") {
		t.Fatalf("cached response did not preserve PII placeholders: %s", cacheRR.Body.String())
	}
	var cachedRow contentCaptureRecord
	if err := svc.usage.db.Where("request_id = ? AND scope = ?", cacheRR.Header().Get("X-Request-Id"), contentCaptureScopeResponse).First(&cachedRow).Error; err != nil {
		t.Fatal(err)
	}
	cachedPlaintext := testContentCapturePlaintext(t, cachedRow.ContentText, cachedRow.EncryptionNonce, cachedRow.EncryptionLocalKeyID)
	if strings.Contains(cachedRow.ContentText, "jane.alt@example.com") || strings.Contains(cachedPlaintext, "jane.alt@example.com") {
		t.Fatalf("cached response capture stored restored PII: %s", cachedRow.ContentText)
	}
	if !strings.Contains(cachedPlaintext, "[EMAIL_1]") {
		t.Fatalf("cached response capture missing placeholder: %s", cachedPlaintext)
	}
}

func TestPIIFilterFailOnMatchRejectsBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{
		Strategy:  "static",
		PIIFilter: testPIIFilterConfig("fail_on_match"),
		Targets:   []Target{{Provider: "mock", Model: "mock-model"}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"SSN 123-45-6789"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "pii-filter-blocked") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls.Load())
	}
}

func TestPIIFilterRedactsResponsesAndAnthropicMessages(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		dialect      string
		body         string
		wantField    string
		responseBody map[string]any
	}{
		{
			name:      "responses",
			path:      "/v1/responses",
			dialect:   "openai-responses",
			body:      `{"model":"default","input":"Email jane.doe@example.com","max_output_tokens":16}`,
			wantField: "input",
			responseBody: map[string]any{
				"id":          "resp_pii",
				"output_text": "Received [EMAIL_1].",
				"usage":       map[string]any{"input_tokens": 4, "output_tokens": 3, "total_tokens": 7},
			},
		},
		{
			name:      "anthropic",
			path:      "/v1/messages",
			dialect:   "anthropic",
			body:      `{"model":"default","messages":[{"role":"user","content":"Email jane.doe@example.com"}],"max_tokens":16}`,
			wantField: "messages",
			responseBody: map[string]any{
				"id":          "msg_pii",
				"type":        "message",
				"content":     []map[string]any{{"type": "text", "text": "Received [EMAIL_1]."}},
				"stop_reason": "end_turn",
				"usage":       map[string]any{"input_tokens": 4, "output_tokens": 3},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamBody map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(upstreamBody)
				if strings.Contains(string(raw), "jane.doe@example.com") {
					t.Fatalf("upstream received raw PII: %s", raw)
				}
				writeJSON(w, http.StatusOK, tc.responseBody)
			}))
			defer upstream.Close()
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: tc.dialect, APIKey: "provider-key"}
			cfg.Models["default"] = ModelGroup{
				Strategy:  "static",
				PIIFilter: testPIIFilterConfig("redact_only"),
				Targets:   []Target{{Provider: "mock", Model: "mock-model"}},
			}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "[EMAIL_1]") {
				t.Fatalf("response did not preserve placeholder: %s", rr.Body.String())
			}
			raw, _ := json.Marshal(upstreamBody[tc.wantField])
			if !strings.Contains(string(raw), "[EMAIL_1]") {
				t.Fatalf("upstream %s=%s, want placeholder", tc.wantField, raw)
			}
		})
	}
}

func TestPIIFilterRedactsOpenAIChatToolResultPassthrough(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(upstreamBody)
		if strings.Contains(string(raw), "jane.doe@example.com") {
			t.Fatalf("upstream received raw tool-result PII: %s", raw)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "pii_tool_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "Tool result had [EMAIL_1]."},
			}},
			"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 5, "total_tokens": 13},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["default"] = ModelGroup{
		Strategy:  "static",
		PIIFilter: testPIIFilterConfig("redact_only"),
		Targets: []Target{{
			Provider:    "mock",
			Model:       "mock-model",
			ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}},
		}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{
	  "model":"default",
	  "messages":[
	    {"role":"user","content":"Use the tool result."},
	    {"role":"tool","tool_call_id":"call_123","content":"Customer email jane.doe@example.com"}
	  ],
	  "tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}],
	  "tool_choice":"auto"
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "[EMAIL_1]") {
		t.Fatalf("response did not preserve placeholder: %s", rr.Body.String())
	}
	msgs := upstreamBody["messages"].([]any)
	toolMsg := msgs[1].(map[string]any)
	if toolMsg["tool_call_id"] != "call_123" {
		t.Fatalf("tool_call_id changed: %#v", toolMsg)
	}
	if content := toolMsg["content"].(string); !strings.Contains(content, "[EMAIL_1]") || strings.Contains(content, "jane.doe@example.com") {
		t.Fatalf("tool content=%q, want redacted placeholder", content)
	}
}

func TestPIIFilterRedactOnlyPreservesPlaceholdersAndImages(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(upstreamBody)
		if strings.Contains(string(raw), "jane.doe@example.com") {
			t.Fatalf("upstream received raw PII: %s", raw)
		}
		if !strings.Contains(string(raw), "https://example.com/receipt.png") {
			t.Fatalf("upstream did not preserve image URL: %s", raw)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "pii_image_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "Image processed for [EMAIL_1]."},
			}},
			"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{
		Strategy:  "static",
		PIIFilter: testPIIFilterConfig("redact_only"),
		Targets: []Target{{
			Provider:        "mock",
			Model:           "mock-model",
			InputModalities: []string{"text", "image"},
		}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{
	  "model":"default",
	  "messages":[{
	    "role":"user",
	    "content":[
	      {"type":"text","text":"Receipt for jane.doe@example.com"},
	      {"type":"image_url","image_url":{"url":"https://example.com/receipt.png"}}
	    ]
	  }],
	  "max_tokens":64,
	  "stream":false
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "jane.doe@example.com") || !strings.Contains(rr.Body.String(), "[EMAIL_1]") {
		t.Fatalf("redact_only response=%s, want placeholder without original", rr.Body.String())
	}
	msgs := upstreamBody["messages"].([]any)
	parts := msgs[0].(map[string]any)["content"].([]any)
	textPart := parts[0].(map[string]any)
	imagePart := parts[1].(map[string]any)
	if text := textPart["text"].(string); !strings.Contains(text, "[EMAIL_1]") || strings.Contains(text, "jane.doe@example.com") {
		t.Fatalf("text part=%q, want redacted placeholder", text)
	}
	imageURL := imagePart["image_url"].(map[string]any)["url"].(string)
	if imageURL != "https://example.com/receipt.png" {
		t.Fatalf("image URL changed: %q", imageURL)
	}
}

func TestPIIFilterImageURLsRedactedOnlyWhenEnabled(t *testing.T) {
	tests := []struct {
		name        string
		imageURLs   bool
		wantURLPart string
		blockURL    string
	}{
		{
			name:        "disabled",
			imageURLs:   false,
			wantURLPart: "jane.doe@example.com",
			blockURL:    "[EMAIL_1]",
		},
		{
			name:        "enabled",
			imageURLs:   true,
			wantURLPart: "[EMAIL_1]",
			blockURL:    "jane.doe@example.com",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamBody map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
					t.Fatal(err)
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"id": "pii_image_url_1",
					"choices": []map[string]any{{
						"message": map[string]any{"role": "assistant", "content": "ok"},
					}},
					"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 1, "total_tokens": 9},
				})
			}))
			defer upstream.Close()

			filter := testPIIFilterConfig("redact_only")
			filter.ApplyTo.ImageURLs = tc.imageURLs
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Models["default"] = ModelGroup{
				Strategy:  "static",
				PIIFilter: filter,
				Targets: []Target{{
					Provider:        "mock",
					Model:           "mock-model",
					InputModalities: []string{"text", "image"},
				}},
			}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			body := `{
			  "model":"default",
			  "messages":[{
			    "role":"user",
			    "content":[
			      {"type":"text","text":"Read this image."},
			      {"type":"image_url","image_url":{"url":"https://example.com/jane.doe@example.com/receipt.png"}}
			    ]
			  }],
			  "max_tokens":64,
			  "stream":false
			}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			msgs := upstreamBody["messages"].([]any)
			parts := msgs[0].(map[string]any)["content"].([]any)
			imagePart := parts[1].(map[string]any)
			imageURL := imagePart["image_url"].(map[string]any)["url"].(string)
			if !strings.Contains(imageURL, tc.wantURLPart) || strings.Contains(imageURL, tc.blockURL) {
				t.Fatalf("image URL=%q, want %q and not %q", imageURL, tc.wantURLPart, tc.blockURL)
			}
		})
	}
}

func TestScriptTargetsIncludeProviderMetadataWithoutRawKeys(t *testing.T) {
	targets := []Target{{
		Provider:    "mock",
		ModelRef:    "small",
		Model:       "mock-small",
		Weight:      7,
		Tier:        "cheap",
		RPM:         12,
		Cost:        3,
		Dialect:     "openai-responses",
		DisplayName: "Mock Small",
		Reasoning: ReasoningSupport{
			Supported: true,
			Mode:      reasoningModeOptIn,
			Control:   reasoningControlEffortEnum,
		},
	}}
	providers := map[string]ProviderConfig{
		"mock": {
			BaseURL:   "https://mock.example/v1",
			Dialect:   "openai-chat",
			APIKey:    "raw-secret-key",
			APIKeyEnv: "MOCK_API_KEY",
			KeyID:     "mock-key",
		},
	}
	scriptTargets := buildScriptTargets(targets, providers)
	if len(scriptTargets) != 1 {
		t.Fatalf("script target count=%d", len(scriptTargets))
	}
	got := scriptTargets[0]
	if got.Model != "mock-small" || got.ModelRef != "small" || got.BaseURL != "https://mock.example/v1" || got.Dialect != "openai-responses" {
		t.Fatalf("metadata not populated: %#v", got)
	}
	if got.Weight != 7 || got.KeyID != "mock-key" || got.APIKeyEnv != "MOCK_API_KEY" || !got.KeyConfigured {
		t.Fatalf("key/weight metadata not populated: %#v", got)
	}
	if !got.Reasoning.Supported || got.Reasoning.Mode != reasoningModeOptIn || got.Reasoning.Control != reasoningControlEffortEnum {
		t.Fatalf("reasoning metadata not populated: %#v", got.Reasoning)
	}
	raw, err := json.Marshal(scriptTargets)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "raw-secret-key") {
		t.Fatalf("raw API key leaked into script context: %s", raw)
	}
}

func TestScriptCallerMetadataExcludesSecrets(t *testing.T) {
	sum := sha256.Sum256([]byte(testToken))
	caller := &callerRuntime{cfg: CallerConfig{
		ID:          "alice",
		OwnerUser:   "Alice",
		Project:     "Metrum Insights",
		Environment: "Prod",
		TokenSHA256: hex.EncodeToString(sum[:]),
		TokenID:     "rtr_metrum_alice_metrum-insights_prod_key1",
		Allow:       []string{"default"},
	}, keyStatus: "active", membershipRole: "developer"}

	scriptCaller := buildScriptCaller(caller, caller.cfg.TokenID)
	if scriptCaller == nil || scriptCaller.TokenID != caller.cfg.TokenID || scriptCaller.User != "alice" || scriptCaller.OwnerUser != "alice" || scriptCaller.Username != "alice" || scriptCaller.Project != "metrum-insights" || scriptCaller.Environment != "prod" || scriptCaller.MembershipRole != "developer" || scriptCaller.KeyStatus != "active" {
		t.Fatalf("caller metadata not populated: %#v", scriptCaller)
	}
	raw, err := json.Marshal(scriptCaller)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testToken) || strings.Contains(string(raw), caller.cfg.TokenSHA256) {
		t.Fatalf("caller secret leaked into script context: %s", raw)
	}
}

func TestModelRefTargetUsesResolvedExternalModel(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "model_ref_1",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "resolved"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai",
		APIKey:  "provider-key",
		Models: map[string]ProviderModel{
			"small": {Model: "mock-small"},
		},
	}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", ModelRef: "small"}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotModel != "mock-small" {
		t.Fatalf("upstream model=%q", gotModel)
	}
}

func TestToolRequestsRequireMatchingToolSupportMetadata(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{
		BaseURL: "http://127.0.0.1:1/v1",
		Dialect: "openai-responses",
		APIKey:  "provider-key",
		Models: map[string]ProviderModel{
			"chat-tools-only": {
				Model: "chat-tools-only",
				ToolSupport: ToolSupport{
					OpenAIChat: []string{"tools", "tool_choice"},
				},
			},
			"responses-tools": {
				Model: "responses-tools",
				ToolSupport: ToolSupport{
					OpenAIResponses: []string{"function"},
				},
			},
		},
	}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", ModelRef: "chat-tools-only", ToolOnly: true}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.supportedToolsForGroup("default"); len(got) != 0 {
		t.Fatalf("advertised tools for incompatible metadata: %#v", got)
	}
	svc.Close()

	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", ModelRef: "responses-tools", ToolOnly: true}}}
	svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got := svc.supportedToolsForGroup("default"); len(got) == 0 {
		t.Fatal("expected tools for matching responses metadata")
	}
}

func TestResponsesTrafficIgnoresChatSkinEvenWithResponsesCatalogMetadata(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"shared-upstream-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider = map[string]ProviderConfig{
		"chat_skin": {
			BaseURL: upstream.URL + "/v1",
			Dialect: "openai-chat",
			APIKey:  "provider-key",
			Models: map[string]ProviderModel{
				"shared": {
					Model: "shared-upstream-model",
					ToolSupport: ToolSupport{
						OpenAIChat:      []string{"tools"},
						OpenAIResponses: []string{"function"},
					},
				},
			},
		},
	}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "chat_skin", ModelRef: "shared"}}}
	delete(cfg.Models, "other")
	cfg.Callers[0].Allow = []string{"default"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"default","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	svc.Close()
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) {
		t.Fatalf("status=%d body=%s, want no eligible target", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("chat skin should not be selected for responses tools, upstreamCalls=%d", upstreamCalls)
	}

	cfg.Provider["responses_skin"] = ProviderConfig{
		BaseURL: upstream.URL + "/v1",
		Dialect: "openai-responses",
		APIKey:  "provider-key",
		Models: map[string]ProviderModel{
			"shared": {
				Model:       "shared-upstream-model",
				ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
			},
		},
	}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "chat_skin", ModelRef: "shared"}, {Provider: "responses_skin", ModelRef: "shared", ToolOnly: true}}}
	svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"default","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr = httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want responses skin selected", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected one responses-skin upstream call, got %d", upstreamCalls)
	}
}

func TestOpenRouterAnthropicSkinUsesBearerAuthAndMessagesPath(t *testing.T) {
	var gotPath, gotAuth, gotAPIKey, gotVersion, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":            "msg_openrouter",
			"type":          "message",
			"role":          "assistant",
			"model":         gotModel,
			"content":       []map[string]any{{"type": "text", "text": "openrouter anthropic ok"}},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 1, "output_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "unused", t.TempDir())
	cfg.Provider["openrouter_anthropic"] = ProviderConfig{
		BaseURL:    upstream.URL + "/api",
		Dialect:    "anthropic",
		AuthScheme: "bearer",
		APIKey:     "openrouter-key",
		Models: map[string]ProviderModel{
			"qwen3-coder-30b-nitro": {
				Model:            "qwen/qwen3-coder-30b-a3b-instruct:nitro",
				Weight:           1,
				InputModalities:  []string{"text", "image"},
				OutputModalities: []string{"text"},
				ToolSupport:      ToolSupport{AnthropicMessages: []string{"client_tools"}},
			},
		},
	}
	cfg.Models["openrouter-anthropic"] = ModelGroup{
		Strategy: "static",
		Targets:  []Target{{Provider: "openrouter_anthropic", ModelRef: "qwen3-coder-30b-nitro"}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "openrouter-anthropic")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"openrouter-anthropic","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/api/v1/messages" {
		t.Fatalf("unexpected path %s", gotPath)
	}
	if gotAuth != "Bearer openrouter-key" || gotAPIKey != "" {
		t.Fatalf("unexpected auth headers Authorization=%q X-API-Key=%q", gotAuth, gotAPIKey)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("missing Anthropic-Version, got %q", gotVersion)
	}
	if gotModel != "qwen/qwen3-coder-30b-a3b-instruct:nitro" {
		t.Fatalf("upstream model=%q", gotModel)
	}
}

func TestAnthropicToolPassthroughAppliesDefaultThinking(t *testing.T) {
	var gotThinking map[string]any
	var gotToolChoice any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotThinking, _ = body["thinking"].(map[string]any)
		gotToolChoice = body["tool_choice"]
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "msg_kimi",
			"type":        "message",
			"role":        "assistant",
			"model":       body["model"],
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "unused", t.TempDir())
	cfg.Provider["kimi_anthropic"] = ProviderConfig{BaseURL: upstream.URL + "/anthropic", Dialect: "anthropic", AuthScheme: "bearer", APIKey: "kimi-key"}
	cfg.Models["kimi-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "kimi_anthropic",
		Model:           "kimi-k2.7-code",
		ToolOnly:        true,
		DefaultThinking: map[string]any{"type": "enabled", "budget_tokens": 512},
		ToolSupport:     ToolSupport{AnthropicMessages: []string{"client_tools"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "kimi-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"kimi-tools","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"echo","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"echo"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotThinking["type"] != "enabled" || gotThinking["budget_tokens"].(float64) != 512 {
		t.Fatalf("thinking=%#v, want enabled budget 512", gotThinking)
	}
	if gotToolChoice != nil {
		t.Fatalf("tool_choice=%#v, want omitted for Kimi thinking compatibility", gotToolChoice)
	}
}

func TestOpenAIChatToolPassthroughAppliesTargetDefaultThinking(t *testing.T) {
	var gotThinking map[string]any
	var gotToolChoice any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotThinking, _ = body["thinking"].(map[string]any)
		gotToolChoice = body["tool_choice"]
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_kimi",
			"object":  "chat.completion",
			"model":   body["model"],
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["kimi-k3-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:                  "mock",
		Model:                     "kimi-k3",
		DefaultOpenAIChatThinking: map[string]any{"type": "disabled"},
		ToolSupport:               ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "kimi-k3-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	request := func(thinking string) {
		body := `{"model":"kimi-k3-tools","messages":[{"role":"user","content":"use the tool"}],"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"echo"}}}`
		if thinking != "" {
			body = strings.TrimSuffix(body, `}`) + `,"thinking":{"type":"` + thinking + `"}}`
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}

	request("")
	if gotThinking["type"] != "disabled" {
		t.Fatalf("thinking=%#v, want target default disabled", gotThinking)
	}
	if gotToolChoice == nil {
		t.Fatal("tool_choice was removed, want caller option preserved")
	}

	request("enabled")
	if gotThinking["type"] != "enabled" {
		t.Fatalf("thinking=%#v, want caller value preserved", gotThinking)
	}
}

func TestResponsesToolPassthroughUsesMiniMaxResponsesProviderSkin(t *testing.T) {
	var gotPath, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_minimax",
			"object":      "response",
			"status":      "completed",
			"model":       gotModel,
			"output_text": "ok",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "unused", t.TempDir())
	cfg.Provider["minimax"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "minimax-key"}
	cfg.Provider["minimax_responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "minimax-key"}
	cfg.Models["agent-tools-smoke"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "minimax_responses", Model: "MiniMax-M3", ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "agent-tools-smoke")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"agent-tools-smoke","input":"hi","tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/responses" || gotModel != "MiniMax-M3" {
		t.Fatalf("path/model=%s/%s, want /v1/responses MiniMax-M3", gotPath, gotModel)
	}
}

func TestResponsesToolPassthroughSkipsMiniMaxForcedToolChoice(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "unused", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["minimax_responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "minimax-key"}
	cfg.Models["agent-tools-smoke"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "minimax_responses",
		Model:    "MiniMax-M3",
		ToolSupport: ToolSupport{
			OpenAIResponses: []string{"function"},
		},
		RequestShapeSupport: RequestShapeSupport{
			UnsupportedRequestFeatures: []string{"forced_tool_choice"},
		},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "agent-tools-smoke")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"agent-tools-smoke","input":"hi","tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"echo"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalled {
		t.Fatal("upstream called for MiniMax forced tool_choice despite unsupported request-shape metadata")
	}
	if !strings.Contains(rr.Body.String(), "no-eligible-target") {
		t.Fatalf("body=%s, want no-eligible-target", rr.Body.String())
	}
}

func TestResponsesToChatBridgeRequiresOptIn(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["bridge"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "chat", Model: "chat-model"}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"bridge","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalled {
		t.Fatal("Chat upstream called without responses_to_chat opt-in")
	}
	assertDecisionFilterReason(t, svc, "responses-to-chat-bridge-disabled")
}

func TestResponsesToChatBridgeTextAndFunctionToolsHandler(t *testing.T) {
	var gotPath string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_bridge",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   stringValue(upstreamBody["model"]),
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "bridged ok",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["bridge"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "chat",
		Model:           "chat-model",
		ToolSupport:     ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true, ToolChoice: true, ValidationStatus: "passed"},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"bridge","instructions":"Be brief.","input":"hi","max_output_tokens":4,"tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}],"tool_choice":"auto"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("upstream path=%q, want chat completions", gotPath)
	}
	if upstreamBody["model"] != "chat-model" || upstreamBody["max_tokens"] != float64(4) {
		t.Fatalf("upstream body=%#v", upstreamBody)
	}
	if _, ok := upstreamBody["max_output_tokens"]; ok {
		t.Fatalf("max_output_tokens leaked to Chat upstream: %#v", upstreamBody)
	}
	tools := upstreamBody["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("translated tools=%#v", tools)
	}
	var downstream map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &downstream); err != nil {
		t.Fatal(err)
	}
	if downstream["object"] != "response" || downstream["output_text"] != "bridged ok" {
		t.Fatalf("downstream=%#v", downstream)
	}

	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.InboundDialect != "openai-responses" || usage.TargetDialect != "openai-chat" || usage.TargetProvider != "chat" || usage.TargetModel != "chat-model" {
		t.Fatalf("usage dialect/provider/model mismatch: %#v", usage)
	}
	var shape requestTranslationShapeRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	if shape.BridgeDirection != "responses_to_chat" || shape.Dialect != "openai-chat" || shape.TranslatedToolCount != 1 || shape.TranslatedOutputCapField != "max_tokens" {
		t.Fatalf("translation shape=%#v", shape)
	}
}

func TestResponsesToChatBridgeSkipsUnsupportedBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["bridge"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "chat",
		Model:           "chat-model",
		ToolSupport:     ToolSupport{OpenAIChat: []string{"tools"}},
		ResponsesToChat: ResponsesToChatBridge{Enabled: true, Text: true, FunctionTools: true},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "previous response", body: `{"model":"bridge","input":"hi","previous_response_id":"resp_1"}`, want: "responses-to-chat-previous-response-id"},
		{name: "hosted tool", body: `{"model":"bridge","input":"hi","tools":[{"type":"web_search"}]}`, want: "responses-to-chat-hosted-tools"},
		{name: "no tools explicit null tool choice", body: `{"model":"bridge","input":"hi","tool_choice":null}`, want: "responses-to-chat-tool-choice-null-unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			assertDecisionFilterReason(t, svc, tc.want)
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want none", calls.Load())
	}
}

func TestResponsesToolPassthroughRequiresExplicitTargetSupport(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["responses-no-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "responses", Model: "responses-plain"}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "responses-no-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"responses-no-tools","input":"hi","tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) {
		t.Fatalf("body=%s, want no-eligible-target", rr.Body.String())
	}
	if upstreamCalled {
		t.Fatal("upstream called for Responses tool request without explicit tool support")
	}
}

func TestOpenAIResponsesRejectsRemoteProviderHostedToolsBeforeUpstream(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["responses-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "responses",
		Model:       "responses-tool-model",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "responses-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "mcp",
			body: `{"model":"responses-tools","input":"hi","tools":[{"type":"mcp","server_url":"https://example.com/mcp"}]}`,
		},
		{
			name: "file_search",
			body: `{"model":"responses-tools","input":"hi","tools":[{"type":"file_search","vector_store_ids":["vs_123"]}]}`,
		},
		{
			name: "code_interpreter",
			body: `{"model":"responses-tools","input":"hi","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), `"type":"provider-hosted-tools-forbidden"`) {
				t.Fatalf("body=%s, want provider-hosted-tools-forbidden", rr.Body.String())
			}
			if upstreamCalls != 0 {
				t.Fatal("upstream called for provider-hosted Responses tool")
			}
		})
	}
}

func TestOpenAIResponsesRejectsUnadvertisedBackgroundAndWebSocket(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "resp_should_not_run",
			"object": "response",
			"status": "completed",
			"model":  "synthetic",
			"output": []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "nope"}}}},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["responses"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "responses",
		Model:       "synthetic-responses",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "responses")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	t.Run("background", func(t *testing.T) {
		upstreamCalls = 0
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"responses","input":"hi","background":true}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"type":"responses-background-unsupported"`) {
			t.Fatalf("body=%s, want responses-background-unsupported", rr.Body.String())
		}
		if upstreamCalls != 0 {
			t.Fatal("upstream called for background Responses request")
		}
	})

	t.Run("websocket_upgrade", func(t *testing.T) {
		upstreamCalls = 0
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"responses","input":"hi"}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"type":"responses-websocket-unsupported"`) {
			t.Fatalf("body=%s, want responses-websocket-unsupported", rr.Body.String())
		}
		if upstreamCalls != 0 {
			t.Fatal("upstream called for WebSocket Responses request")
		}
	})

	t.Run("background_false_allowed", func(t *testing.T) {
		upstreamCalls = 0
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"responses","input":"hi","background":false}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if upstreamCalls != 1 {
			t.Fatalf("upstreamCalls=%d, want 1", upstreamCalls)
		}
	})
}

func TestOpenAIResponsesStripsGenericHostedToolsBeforeUpstream(t *testing.T) {
	var gotTools []any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotTools, _ = body["tools"].([]any)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "resp_tool_filter",
			"object": "response",
			"status": "completed",
			"model":  body["model"],
			"output": []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "OK"}}}},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["responses-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "responses",
		Model:       "responses-tool-model",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "responses-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"responses-tools","input":"hi","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"namespace","name":"multi_tool_use","tools":[]},{"type":"web_search","external_web_access":false},{"type":"image_generation","output_format":"png"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(gotTools) != 2 {
		t.Fatalf("upstream tools=%#v, want function and namespace only", gotTools)
	}
	gotTypes := []string{}
	for _, rawTool := range gotTools {
		tool, _ := rawTool.(map[string]any)
		gotTypes = append(gotTypes, stringValue(tool["type"]))
	}
	if !stringSliceEqual(gotTypes, []string{"function", "namespace"}) {
		t.Fatalf("upstream tool types=%#v", gotTypes)
	}
}

func TestOpenAIResponsesForceStoreFalseOverridesCallerStore(t *testing.T) {
	var gotStore any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotStore = body["store"]
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "resp_store_false",
			"object": "response",
			"status": "completed",
			"model":  body["model"],
			"output": []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "OK"}}}},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["responses-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "responses",
		Model:           "responses-tool-model",
		ToolSupport:     ToolSupport{OpenAIResponses: []string{"function"}},
		ForceStoreFalse: true,
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "responses-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"responses-tools","input":"hi","store":true,"tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotStore != false {
		t.Fatalf("upstream store=%#v, want false", gotStore)
	}
}

func TestOpenAIResponsesForceStoreFalseAppliesToTranslatedText(t *testing.T) {
	var gotStore any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotStore = body["store"]
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "resp_store_false_text",
			"object": "response",
			"status": "completed",
			"model":  body["model"],
			"output": []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "OK"}}}},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["responses-text"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "responses",
		Model:           "responses-text-model",
		ForceStoreFalse: true,
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "responses-text")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"responses-text","input":"hi","store":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotStore != false {
		t.Fatalf("upstream store=%#v, want false", gotStore)
	}
}

func TestChatInboundCanUseOptInResponsesBridgeText(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_bridge_text",
			"object":      "response",
			"status":      "completed",
			"model":       stringValue(upstreamBody["model"]),
			"output_text": "bridge ok",
			"usage":       map[string]any{"input_tokens": 7, "output_tokens": 3, "total_tokens": 10},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["bridge"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "responses",
		Model:    "responses-text-model",
		Bridges:  BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"bridge","messages":[{"role":"system","content":"private system"},{"role":"user","content":"hello bridge"}],"max_tokens":16}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["model"] != "responses-text-model" || upstreamBody["instructions"] != "private system" || upstreamBody["max_output_tokens"].(float64) != 16 {
		t.Fatalf("upstream body=%#v", upstreamBody)
	}
	if _, ok := upstreamBody["messages"]; ok {
		t.Fatalf("bridge sent chat messages upstream: %#v", upstreamBody)
	}
	var chat map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}
	if chat["object"] != "chat.completion" || !strings.Contains(rr.Body.String(), "bridge ok") {
		t.Fatalf("chat response=%s", rr.Body.String())
	}
	var usage usageRecord
	if err := svc.usage.db.First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.InboundDialect != "openai-chat" || usage.TargetDialect != "openai-responses" || usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("usage=%#v", usage)
	}
	var translated requestTranslationShapeRecord
	if err := svc.usage.db.Where("request_id = ? AND attempt_index = ?", usage.RequestID, 1).First(&translated).Error; err != nil {
		t.Fatal(err)
	}
	if translated.Dialect != "openai-responses" || translated.EndpointPath != "/v1/responses" || translated.TranslatedOutputCapField != "max_output_tokens" {
		t.Fatalf("translated=%#v", translated)
	}
	var events []requestTranslationFieldEventRecord
	if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	assertPersistedShapeRowsDoNotContain(t, requestShapeRecord{}, translated, events, "private system", "hello bridge", "provider-key", testToken)
}

func TestChatInboundResponsesBridgeRequiresOptIn(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["bridge-disabled"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "responses", Model: "responses-text-model"}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge-disabled")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge-disabled","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalled {
		t.Fatal("upstream called without bridge opt-in")
	}
	assertDecisionFilterReason(t, svc, "chat-to-responses-bridge-disabled")
}

func TestChatInboundResponsesBridgeStatefulSessionInjectsPreviousResponseID(t *testing.T) {
	var upstreamBodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstreamBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		upstreamBodies = append(upstreamBodies, upstreamBody)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          fmt.Sprintf("resp_bridge_stateful_%d", len(upstreamBodies)),
			"object":      "response",
			"status":      "completed",
			"model":       stringValue(upstreamBody["model"]),
			"output_text": "stateful ok",
			"usage":       map[string]any{"input_tokens": 5, "output_tokens": 2, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["bridge-stateful"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "responses",
		Model:    "responses-text-model",
		Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{
			Enabled: true,
			StatefulSessions: BridgeStatefulSessionsConfig{
				Enabled:       true,
				SessionHeader: "X-Router-Session",
				TTLSeconds:    60,
				MaxEntries:    10,
			},
		}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge-stateful")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, content := range []string{"first", "second"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"bridge-stateful","messages":[{"role":"user","content":%q}],"max_tokens":16}`, content)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("X-Router-Session", "session-alpha")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream calls=%d", len(upstreamBodies))
	}
	if _, ok := upstreamBodies[0]["previous_response_id"]; ok {
		t.Fatalf("first request had previous_response_id: %#v", upstreamBodies[0])
	}
	if got := upstreamBodies[1]["previous_response_id"]; got != "resp_bridge_stateful_1" {
		t.Fatalf("second previous_response_id=%#v body=%#v", got, upstreamBodies[1])
	}
}

func TestChatInboundResponsesBridgeSharedBackendContinuesAcrossServices(t *testing.T) {
	var upstreamBodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstreamBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		upstreamBodies = append(upstreamBodies, upstreamBody)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          fmt.Sprintf("resp_shared_%d", len(upstreamBodies)),
			"object":      "response",
			"status":      "completed",
			"model":       stringValue(upstreamBody["model"]),
			"output_text": "shared ok",
			"usage":       map[string]any{"input_tokens": 5, "output_tokens": 2, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	store := newTestSharedBridgeSessionStore()
	svc1 := newSharedBackendBridgeService(t, upstream.URL, store, t.TempDir())
	defer svc1.Close()
	svc2 := newSharedBackendBridgeService(t, upstream.URL, store, t.TempDir())
	defer svc2.Close()

	for i, svc := range []*Service{svc1, svc2} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"bridge-stateful","messages":[{"role":"user","content":"turn %d"}],"max_tokens":16}`, i+1)))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("X-Router-Session", "session-alpha")
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("turn %d status=%d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}
	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream calls=%d", len(upstreamBodies))
	}
	if _, ok := upstreamBodies[0]["previous_response_id"]; ok {
		t.Fatalf("first request had previous_response_id: %#v", upstreamBodies[0])
	}
	if got := upstreamBodies[1]["previous_response_id"]; got != "resp_shared_1" {
		t.Fatalf("second previous_response_id=%#v body=%#v", got, upstreamBodies[1])
	}
	for _, key := range store.keys() {
		if strings.Contains(key, "session-alpha") || strings.Contains(key, testToken) {
			t.Fatalf("shared backend key leaked raw session or token: %q", key)
		}
	}
}

func TestChatInboundResponsesBridgeSharedBackendStaleRetryPurgesMapping(t *testing.T) {
	var upstreamBodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstreamBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		upstreamBodies = append(upstreamBodies, upstreamBody)
		if upstreamBody["previous_response_id"] == "resp_stale_shared" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "previous_response_id was not found"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          fmt.Sprintf("resp_shared_recovered_%d", len(upstreamBodies)),
			"object":      "response",
			"status":      "completed",
			"model":       stringValue(upstreamBody["model"]),
			"output_text": "recovered",
			"usage":       map[string]any{"input_tokens": 5, "output_tokens": 2, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	store := newTestSharedBridgeSessionStore()
	dir := t.TempDir()
	svc := newSharedBackendBridgeService(t, upstream.URL, store, dir)
	defer svc.Close()
	rc := &requestContext{rec: logRecord{TokenID: svc.cfg.Callers[0].TokenID}, caller: svc.quota.callers["alice"]}
	target := svc.cfg.Models["bridge-stateful"].Targets[0]
	key := bridgeSessionKey(rc, "bridge-stateful", target, "session-alpha")
	if err := store.Set(context.Background(), key, "resp_stale_shared", time.Minute, 0); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge-stateful","messages":[{"role":"user","content":"recover"}],"max_tokens":16}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Router-Session", "session-alpha")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream calls=%d bodies=%#v", len(upstreamBodies), upstreamBodies)
	}
	if upstreamBodies[0]["previous_response_id"] != "resp_stale_shared" {
		t.Fatalf("first upstream body=%#v", upstreamBodies[0])
	}
	if _, ok := upstreamBodies[1]["previous_response_id"]; ok {
		t.Fatalf("stateless retry still had previous_response_id: %#v", upstreamBodies[1])
	}
	if entry, ok, err := store.Get(context.Background(), key); err != nil || !ok || entry.PreviousResponseID == "resp_stale_shared" {
		t.Fatalf("shared session was not refreshed after stale purge: entry=%#v ok=%v err=%v", entry, ok, err)
	}
	var traces []requestTraceEventRecord
	if err := svc.usage.db.Where("request_id = ?", rr.Header().Get("X-Request-Id")).Order("seq ASC").Find(&traces).Error; err != nil {
		t.Fatal(err)
	}
	if !traceEventsContain(traces, "bridge_session_delete") || !traceEventsContain(traces, "bridge_session_previous_response_stale_purged") || !traceEventsContain(traces, "bridge_session_stateless_retry") {
		t.Fatalf("missing shared stale retry trace events: %#v", traces)
	}
}

func TestChatInboundResponsesBridgeSharedBackendGetOutageContinuesStateless(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_after_outage",
			"object":      "response",
			"status":      "completed",
			"model":       stringValue(upstreamBody["model"]),
			"output_text": "stateless ok",
			"usage":       map[string]any{"input_tokens": 5, "output_tokens": 2, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	store := newTestSharedBridgeSessionStore()
	store.getErr = errors.New("redis unavailable")
	svc := newSharedBackendBridgeService(t, upstream.URL, store, t.TempDir())
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge-stateful","messages":[{"role":"user","content":"outage"}],"max_tokens":16}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Router-Session", "session-alpha")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := upstreamBody["previous_response_id"]; ok {
		t.Fatalf("outage request should continue stateless: %#v", upstreamBody)
	}
	var traces []requestTraceEventRecord
	if err := svc.usage.db.Where("request_id = ?", rr.Header().Get("X-Request-Id")).Order("seq ASC").Find(&traces).Error; err != nil {
		t.Fatal(err)
	}
	if !traceEventsContain(traces, "bridge_session_backend_error") || !traceEventsContain(traces, "bridge_session_set") {
		t.Fatalf("missing outage trace events: %#v", traces)
	}
}

func newSharedBackendBridgeService(t *testing.T, upstreamURL string, store bridgeSessionBackend, dir string) *Service {
	t.Helper()
	cfg := testConfig(t, upstreamURL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstreamURL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["bridge-stateful"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "responses",
		Model:    "responses-text-model",
		Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{
			Enabled: true,
			StatefulSessions: BridgeStatefulSessionsConfig{
				Enabled:       true,
				SessionHeader: "X-Router-Session",
				TTLSeconds:    60,
				MaxEntries:    10,
			},
		}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge-stateful")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	redisCfg := BridgeStatefulSessionsRedisConfig{
		Address:   "redis.example.test:6379",
		Namespace: "test-router",
	}
	group := svc.cfg.Models["bridge-stateful"]
	target := group.Targets[0]
	target.Bridges.ChatToResponses.StatefulSessions.Backend = "redis"
	target.Bridges.ChatToResponses.StatefulSessions.Redis = redisCfg
	group.Targets[0] = target
	svc.cfg.Models["bridge-stateful"] = group
	svc.bridgeSessions.redis[bridgeRedisBackendKey(redisCfg)] = store
	return svc
}

type testSharedBridgeSessionStore struct {
	mu      sync.Mutex
	entries map[string]testSharedBridgeSessionEntry
	now     func() time.Time
	getErr  error
	setErr  error
	delErr  error
	seen    []string
}

type testSharedBridgeSessionEntry struct {
	previousResponseID string
	expiresAt          time.Time
}

func newTestSharedBridgeSessionStore() *testSharedBridgeSessionStore {
	return &testSharedBridgeSessionStore{
		entries: map[string]testSharedBridgeSessionEntry{},
		now:     time.Now,
	}
}

func (s *testSharedBridgeSessionStore) Get(ctx context.Context, key string) (bridgeSessionEntry, bool, error) {
	if s == nil || strings.TrimSpace(key) == "" {
		return bridgeSessionEntry{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, key)
	if s.getErr != nil {
		return bridgeSessionEntry{}, false, s.getErr
	}
	entry, ok := s.entries[key]
	if !ok {
		return bridgeSessionEntry{}, false, nil
	}
	if !entry.expiresAt.IsZero() && !entry.expiresAt.After(s.now()) {
		delete(s.entries, key)
		return bridgeSessionEntry{}, false, nil
	}
	return bridgeSessionEntry{PreviousResponseID: entry.previousResponseID}, true, nil
}

func (s *testSharedBridgeSessionStore) Set(ctx context.Context, key, previousResponseID string, ttl time.Duration, maxEntries int) error {
	if s == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(previousResponseID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, key)
	if s.setErr != nil {
		return s.setErr
	}
	entry := testSharedBridgeSessionEntry{previousResponseID: previousResponseID}
	if ttl > 0 {
		entry.expiresAt = s.now().Add(ttl)
	}
	s.entries[key] = entry
	return nil
}

func (s *testSharedBridgeSessionStore) Delete(ctx context.Context, key string) error {
	if s == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, key)
	if s.delErr != nil {
		return s.delErr
	}
	delete(s.entries, key)
	return nil
}

func (s *testSharedBridgeSessionStore) Close() error {
	return nil
}

func (s *testSharedBridgeSessionStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func TestChatInboundResponsesBridgeReasoningTelemetry(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_bridge_reasoning",
			"object":      "response",
			"status":      "completed",
			"model":       stringValue(upstreamBody["model"]),
			"output_text": "reasoning ok",
			"usage":       map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["bridge-reasoning"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:  "responses",
		Model:     "responses-reasoning-model",
		Reasoning: ReasoningSupport{Supported: true, Mode: reasoningModeOptIn, Control: reasoningControlEffortEnum},
		Bridges:   BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Reasoning: true}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge-reasoning")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge-reasoning","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	reasoning := upstreamBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || upstreamBody["max_output_tokens"] != float64(64) {
		t.Fatalf("upstream body=%#v", upstreamBody)
	}
	var translated requestTranslationShapeRecord
	if err := svc.usage.db.Where("request_id = ?", rr.Header().Get("X-Request-Id")).First(&translated).Error; err != nil {
		t.Fatal(err)
	}
	if translated.BridgeDirection != chatToResponsesBridgeDirection || translated.TranslatedReasoningControl != "reasoning" {
		t.Fatalf("translation proof=%#v", translated)
	}
}

func TestChatInboundResponsesBridgeStatefulSessionStaleRetryPurgesMapping(t *testing.T) {
	var upstreamBodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstreamBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		upstreamBodies = append(upstreamBodies, upstreamBody)
		if upstreamBody["previous_response_id"] == "resp_stale" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "previous_response_id was not found",
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          fmt.Sprintf("resp_recovered_%d", len(upstreamBodies)),
			"object":      "response",
			"status":      "completed",
			"model":       stringValue(upstreamBody["model"]),
			"output_text": "recovered",
			"usage":       map[string]any{"input_tokens": 5, "output_tokens": 2, "total_tokens": 7},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	target := Target{
		Provider: "responses",
		Model:    "responses-text-model",
		Bridges: BridgeSupport{ChatToResponses: DialectBridgeSupport{
			Enabled: true,
			StatefulSessions: BridgeStatefulSessionsConfig{
				Enabled:       true,
				SessionHeader: "X-Router-Session",
				TTLSeconds:    60,
				MaxEntries:    10,
			},
		}},
	}
	cfg.Models["bridge-stateful"] = ModelGroup{Strategy: "static", Targets: []Target{target}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge-stateful")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	rc := &requestContext{rec: logRecord{TokenID: cfg.Callers[0].TokenID}, caller: svc.quota.callers["alice"]}
	key := bridgeSessionKey(rc, "bridge-stateful", target, "session-alpha")
	if err := svc.bridgeSessions.memory.Set(context.Background(), key, "resp_stale", time.Minute, 10); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge-stateful","messages":[{"role":"user","content":"retry without stale state"}],"max_tokens":16}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Router-Session", "session-alpha")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream calls=%d bodies=%#v", len(upstreamBodies), upstreamBodies)
	}
	if upstreamBodies[0]["previous_response_id"] != "resp_stale" {
		t.Fatalf("first upstream body=%#v", upstreamBodies[0])
	}
	if _, ok := upstreamBodies[1]["previous_response_id"]; ok {
		t.Fatalf("stateless retry still had previous_response_id: %#v", upstreamBodies[1])
	}
	if entry, ok, err := svc.bridgeSessions.memory.Get(context.Background(), key); err != nil || !ok || entry.PreviousResponseID == "resp_stale" {
		t.Fatalf("session was not refreshed after stale purge: entry=%#v ok=%v", entry, ok)
	}
	var traces []requestTraceEventRecord
	if err := svc.usage.db.Where("request_id = ?", rr.Header().Get("X-Request-Id")).Order("seq ASC").Find(&traces).Error; err != nil {
		t.Fatal(err)
	}
	if !traceEventsContain(traces, "bridge_session_previous_response_stale_purged") || !traceEventsContain(traces, "bridge_session_stateless_retry") {
		t.Fatalf("missing stale retry trace events: %#v", traces)
	}
	var translations []requestTranslationShapeRecord
	if err := svc.usage.db.Where("request_id = ?", rr.Header().Get("X-Request-Id")).Order("ts ASC, attempt_index ASC").Find(&translations).Error; err != nil {
		t.Fatal(err)
	}
	if len(translations) != 1 || translations[0].BridgeDirection != chatToResponsesBridgeDirection {
		t.Fatalf("translation rows=%d %#v", len(translations), translations)
	}
	var events []requestTranslationFieldEventRecord
	if err := svc.usage.db.Where("request_id = ?", rr.Header().Get("X-Request-Id")).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	assertPersistedShapeRowsDoNotContain(t, requestShapeRecord{}, translations[0], events, "retry without stale state", "session-alpha", "provider-key", testToken)
}

func TestChatInboundResponsesBridgeToolsEndToEnd(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "resp_bridge_tool",
			"object": "response",
			"status": "completed",
			"model":  stringValue(upstreamBody["model"]),
			"output": []map[string]any{{
				"type":      "function_call",
				"call_id":   "call_echo",
				"name":      "echo",
				"arguments": `{"text":"hi"}`,
			}},
			"usage": map[string]any{"input_tokens": 11, "output_tokens": 2, "total_tokens": 13},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["bridge-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:    "responses",
		Model:       "responses-tool-model",
		ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}},
		Bridges:     BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true, Tools: true, ToolChoice: true}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"bridge-tools","messages":[{"role":"user","content":"call echo"}],"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}],"tool_choice":"auto"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(valueAsSlice(upstreamBody["tools"])) != 1 || upstreamBody["tool_choice"] != "auto" {
		t.Fatalf("upstream body=%#v", upstreamBody)
	}
	if !strings.Contains(rr.Body.String(), `"tool_calls"`) || !strings.Contains(rr.Body.String(), `"finish_reason":"tool_calls"`) {
		t.Fatalf("response=%s", rr.Body.String())
	}
}

func TestChatInboundResponsesBridgeRejectsUnsupportedStreamingShapeBeforeUpstream(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["bridge"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "responses",
		Model:    "responses-text-model",
		Bridges:  BridgeSupport{ChatToResponses: DialectBridgeSupport{Enabled: true}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "bridge")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"bridge","stream":true,"n":2,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamCalled {
		t.Fatal("upstream called for unsupported bridge streaming")
	}
	assertDecisionFilterReason(t, svc, "chat-to-responses-unsupported-field")
}

func TestOpenAIChatPassthroughStripsRetentionFields(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl_retention",
			"object":  "chat.completion",
			"created": 1710000000,
			"model":   stringValue(upstreamBody["model"]),
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "OK"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["openai_chat"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["chat-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "openai_chat", Model: "chat-tool", ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "chat-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"chat-tools","messages":[{"role":"user","content":"hi"}],"store":true,"metadata":{"customer":"secret"},"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := upstreamBody["store"]; ok {
		t.Fatalf("upstream store should be omitted for OpenAI-compatible providers: %#v", upstreamBody)
	}
	if _, ok := upstreamBody["metadata"]; ok {
		t.Fatalf("upstream metadata should be stripped: %#v", upstreamBody)
	}
}

func TestOpenAIResponsesPassthroughStripsRetentionFields(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "resp_retention",
			"object": "response",
			"status": "completed",
			"model":  stringValue(upstreamBody["model"]),
			"output": []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "OK"}}}},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["responses"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
	cfg.Models["responses-tools"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "responses",
		Model:           "responses-tool-model",
		ForceStoreFalse: true,
		ToolSupport:     ToolSupport{OpenAIResponses: []string{"function"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "responses-tools")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"responses-tools","input":"hi","store":true,"metadata":{"customer":"secret"},"tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamBody["store"] != false {
		t.Fatalf("upstream store=%#v, want false", upstreamBody["store"])
	}
	if _, ok := upstreamBody["metadata"]; ok {
		t.Fatalf("upstream metadata should be stripped: %#v", upstreamBody)
	}
}

func TestResponsesToolPassthroughCanUseOpenRouterResponsesTarget(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "resp_openrouter",
			"object":      "response",
			"status":      "completed",
			"model":       gotModel,
			"output_text": "ok",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "unused", t.TempDir())
	cfg.Provider["openrouter_responses"] = ProviderConfig{
		BaseURL: upstream.URL + "/api/v1",
		Dialect: "openai-responses",
		APIKey:  "openrouter-key",
		Models: map[string]ProviderModel{
			"qwen3-coder-30b-nitro": {Model: "qwen/qwen3-coder-30b-a3b-instruct:nitro", Weight: 1, ToolSupport: ToolSupport{OpenAIResponses: []string{"function"}}},
		},
	}
	cfg.Models["agent-tools-smoke-openrouter"] = ModelGroup{
		Strategy: "static",
		Targets:  []Target{{Provider: "openrouter_responses", ModelRef: "qwen3-coder-30b-nitro", Dialect: "openai-responses"}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "agent-tools-smoke-openrouter")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"agent-tools-smoke-openrouter","input":"hi","tools":[{"type":"function","name":"echo","parameters":{"type":"object"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/api/v1/responses" || gotModel != "qwen/qwen3-coder-30b-a3b-instruct:nitro" {
		t.Fatalf("path/model=%s/%s, want /api/v1/responses qwen/qwen3-coder-30b-a3b-instruct:nitro", gotPath, gotModel)
	}
	if gotAuth != "Bearer openrouter-key" {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
}

func TestAnthropicToolPassthroughCanUseOpenRouterAnthropicTarget(t *testing.T) {
	var gotPath, gotAuth, gotAPIKey, gotVersion, gotModel string
	var gotContent []any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-API-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotModel, _ = body["model"].(string)
		if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
			if msg, ok := messages[0].(map[string]any); ok {
				gotContent, _ = msg["content"].([]any)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":          "msg_openrouter_tool",
			"type":        "message",
			"role":        "assistant",
			"model":       gotModel,
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "unused", t.TempDir())
	cfg.Provider["openrouter_anthropic"] = ProviderConfig{
		BaseURL:    upstream.URL + "/api",
		Dialect:    "anthropic",
		AuthScheme: "bearer",
		APIKey:     "openrouter-key",
		Models: map[string]ProviderModel{
			"qwen3-coder-30b-nitro": {Model: "qwen/qwen3-coder-30b-a3b-instruct:nitro", Weight: 1},
		},
	}
	cfg.Models["claude-tools-smoke-openrouter"] = ModelGroup{
		Strategy: "static",
		Targets: []Target{{
			Provider:         "openrouter_anthropic",
			ModelRef:         "qwen3-coder-30b-nitro",
			InputModalities:  []string{"text", "image"},
			OutputModalities: []string{"text"},
			ToolSupport:      ToolSupport{AnthropicMessages: []string{"client_tools"}},
		}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "claude-tools-smoke-openrouter")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"claude-tools-smoke-openrouter","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"` + receiptImageURL + `"}}]}],"tools":[{"name":"echo","input_schema":{"type":"object"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotPath != "/api/v1/messages" || gotModel != "qwen/qwen3-coder-30b-a3b-instruct:nitro" {
		t.Fatalf("path/model=%s/%s, want /api/v1/messages qwen/qwen3-coder-30b-a3b-instruct:nitro", gotPath, gotModel)
	}
	if gotAuth != "Bearer openrouter-key" || gotAPIKey != "" {
		t.Fatalf("unexpected auth headers Authorization=%q X-API-Key=%q", gotAuth, gotAPIKey)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("missing Anthropic-Version, got %q", gotVersion)
	}
	if len(gotContent) != 2 {
		t.Fatalf("forwarded content=%#v, want text and image blocks", gotContent)
	}
	imageBlock, _ := gotContent[1].(map[string]any)
	source, _ := imageBlock["source"].(map[string]any)
	if imageBlock["type"] != "image" || source["type"] != "url" || source["url"] != receiptImageURL {
		t.Fatalf("forwarded image block=%#v", imageBlock)
	}
}

func TestNoEligibleTargetReturnsActionableError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when no target supports the request shape")
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Models["vision"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider:        "mock",
		Model:           "vision-chat-only",
		InputModalities: []string{"text", "image"},
		ToolSupport:     ToolSupport{OpenAIChat: []string{"tools"}},
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "vision")

	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{
		"model":"vision",
		"max_tokens":512,
		"tools":[{"name":"noop","description":"No-op","input_schema":{"type":"object","properties":{}}}],
		"messages":[{"role":"user","content":[
			{"type":"text","text":"Read the image."},
			{"type":"image","source":{"type":"url","url":"` + receiptImageURL + `"}}
		]}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) ||
		!strings.Contains(rr.Body.String(), `anthropic_tool_passthrough`) ||
		!strings.Contains(rr.Body.String(), `image`) {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestUpstreamFailureReturnsActionableError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary outage", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	svc := newTestService(t, upstream.URL, "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"type":"upstream-failed"`) ||
		!strings.Contains(rr.Body.String(), `"attempts":1`) ||
		!strings.Contains(rr.Body.String(), `"provider":"mock"`) ||
		!strings.Contains(rr.Body.String(), `upstream status 503`) {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestUpstreamRedirectsAreNotFollowed(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var redirected atomic.Int64
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirected.Add(1)
				_, _ = io.ReadAll(r.Body)
				writeJSON(w, http.StatusOK, map[string]any{"id": "should-not-happen"})
			}))
			defer second.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, second.URL+"/capture", status)
			}))
			defer upstream.Close()

			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"secret prompt"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if redirected.Load() != 0 {
				t.Fatalf("redirect target received %d requests", redirected.Load())
			}
		})
	}
}

func TestNonRetryableUpstream4xxStopsFallback(t *testing.T) {
	var fallbackCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch stringValue(body["model"]) {
		case "bad-request-model":
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "bad request"}})
		case "fallback-model":
			fallbackCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
		default:
			t.Fatalf("unexpected model %v", body["model"])
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "bad-request-model", ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}},
		{Provider: "mock", Model: "fallback-model", ToolSupport: ToolSupport{OpenAIChat: []string{"tools"}}},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	reqBody := `{
		"model":"default",
		"messages":[{"role":"user","content":"do not replay secret prompt"}],
		"tools":[{"type":"function","function":{"name":"noop","description":"No-op","parameters":{"type":"object","properties":{}}}}],
		"tool_choice":"auto",
		"max_tokens":2048
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var downstream map[string]map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &downstream); err != nil {
		t.Fatalf("decode downstream error: %v body=%s", err, rr.Body.String())
	}
	errObj := downstream["error"]
	details, _ := errObj["details"].(map[string]any)
	if errObj["type"] != "upstream-failed" || !strings.Contains(stringValue(errObj["message"]), "rejected the request shape or parameters") {
		t.Fatalf("unexpected downstream error: %#v", errObj)
	}
	if details["reason_code"] != "upstream_bad_request" || details["reason"] != "selected upstream rejected the request shape or parameters" ||
		details["error_class"] != "upstream_bad_request" || details["upstream_status"] != float64(http.StatusBadRequest) ||
		details["tool_count"] != float64(1) || details["tool_choice_mode"] != "auto" ||
		details["output_cap_field"] != "max_tokens" || details["output_cap_bucket"] == "" ||
		details["request_bytes_bucket"] == "" {
		t.Fatalf("unexpected downstream details: %#v", details)
	}
	for _, forbidden := range []string{"do not replay secret prompt", testToken, "provider-key"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("downstream error leaked %q in %s", forbidden, rr.Body.String())
		}
	}
	if fallbackCalls.Load() != 0 {
		t.Fatalf("fallback calls=%d, want 0", fallbackCalls.Load())
	}
	var attempts []requestAttemptRecord
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Retryable || attempts[0].FallbackReason != "upstream_bad_request" {
		t.Fatalf("unexpected attempts: %#v", attempts)
	}
}

func TestUpstreamRequestTooLargeReturnsSafeShapeReason(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":{"message":"payload too large for this route","type":"invalid_request_error"}}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"large private request"}],"max_tokens":64}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var downstream map[string]map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &downstream); err != nil {
		t.Fatalf("decode downstream error: %v body=%s", err, rr.Body.String())
	}
	errObj := downstream["error"]
	details, _ := errObj["details"].(map[string]any)
	if errObj["type"] != "upstream-failed" || !strings.Contains(stringValue(errObj["message"]), "rejected the request size") {
		t.Fatalf("unexpected downstream error: %#v", errObj)
	}
	if details["reason_code"] != "upstream_request_too_large" || details["reason"] != "selected upstream rejected the request size" ||
		details["error_class"] != "upstream_request_too_large" || details["upstream_status"] != float64(http.StatusRequestEntityTooLarge) ||
		details["request_id"] == "" || details["request_bytes_bucket"] == "" {
		t.Fatalf("unexpected downstream details: %#v", details)
	}
	for _, forbidden := range []string{"large private request", testToken, "provider-key", "payload too large for this route"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("downstream error leaked %q in %s", forbidden, rr.Body.String())
		}
	}
}

func TestUpstreamFailureDetailsUseTerminalAttemptDialect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/v1/chat/completions":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"retryable"}}`))
		case "/anthropic/v1/messages":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported message shape","type":"invalid_request_error"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Provider["mock_chat"] = ProviderConfig{BaseURL: upstream.URL + "/chat/v1", Dialect: "openai-chat", APIKey: "provider-key"}
	cfg.Provider["mock_anthropic"] = ProviderConfig{BaseURL: upstream.URL + "/anthropic", Dialect: "anthropic", APIKey: "provider-key"}
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock_chat", Model: "retryable-chat-model"},
		{Provider: "mock_anthropic", Model: "terminal-anthropic-model"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"private prompt"}],"max_tokens":64}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var downstream map[string]map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &downstream); err != nil {
		t.Fatalf("decode downstream error: %v body=%s", err, rr.Body.String())
	}
	details, _ := downstream["error"]["details"].(map[string]any)
	if details["reason_code"] != "upstream_bad_request" || details["target_dialect"] != "anthropic" || details["fallbackUsed"] != true {
		t.Fatalf("unexpected downstream details: %#v", details)
	}
	if strings.Contains(rr.Body.String(), "private prompt") || strings.Contains(rr.Body.String(), "provider-key") || strings.Contains(rr.Body.String(), testToken) {
		t.Fatalf("downstream error leaked sensitive material: %s", rr.Body.String())
	}
}

func TestUpstream403EntitlementFailureFallsBackWithoutCallerRetryability(t *testing.T) {
	var fallbackCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch stringValue(body["model"]) {
		case "restricted-model":
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"code": "not_entitled", "message": "project_restricted for this account"}})
		case "fallback-model":
			fallbackCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "fallback_ok",
				"choices": []map[string]any{{
					"message":       map[string]any{"role": "assistant", "content": "fallback ok"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		default:
			t.Fatalf("unexpected model %v", body["model"])
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "restricted-model"},
		{Provider: "mock", Model: "fallback-model"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"route around access failure"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls=%d, want 1", fallbackCalls.Load())
	}
	var attempts []requestAttemptRecord
	if err := svc.usage.db.Order("attempt_index ASC").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts=%#v", attempts)
	}
	if attempts[0].ErrorClass != "upstream_entitlement_failed" || attempts[0].Retryable || attempts[0].FallbackReason != "upstream_entitlement_failed" {
		t.Fatalf("unexpected first attempt: %#v", attempts[0])
	}
	if !attempts[1].Selected {
		t.Fatalf("fallback attempt not selected: %#v", attempts[1])
	}
	var transitions []fallbackTransitionRecord
	if err := svc.usage.db.Find(&transitions).Error; err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 1 || transitions[0].FallbackReason != "upstream_entitlement_failed" || transitions[0].Retryable || !transitions[0].FallbackSucceeded {
		t.Fatalf("unexpected fallback transitions: %#v", transitions)
	}
}

func TestUpstreamAuthFailureStopsFallbackWithAccessDeniedError(t *testing.T) {
	var fallbackCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch stringValue(body["model"]) {
		case "bad-key-model":
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "invalid_api_key", "message": "invalid API key provider-secret-value"}})
		case "fallback-model":
			fallbackCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
		default:
			t.Fatalf("unexpected model %v", body["model"])
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "bad-key-model"},
		{Provider: "mock", Model: "fallback-model"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"do not retry bad credentials"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"type":"upstream-access-denied"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "provider-secret-value") || strings.Contains(rr.Body.String(), "provider-key") {
		t.Fatalf("response leaked secret: %s", rr.Body.String())
	}
	if fallbackCalls.Load() != 0 {
		t.Fatalf("fallback calls=%d, want 0", fallbackCalls.Load())
	}
	var attempts []requestAttemptRecord
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].ErrorClass != "upstream_auth_failed" || attempts[0].Retryable || attempts[0].FallbackReason != "upstream_auth_failed" {
		t.Fatalf("unexpected attempts: %#v", attempts)
	}
	var errors []requestErrorRecord
	if err := svc.usage.db.Find(&errors).Error; err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 || errors[0].ErrorType != "upstream-access-denied" || errors[0].Status != http.StatusServiceUnavailable || errors[0].Retryable {
		t.Fatalf("unexpected errors: %#v", errors)
	}
}

func TestRetryableUpstream5xxStillFallsBack(t *testing.T) {
	var fallbackCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch stringValue(body["model"]) {
		case "temporary-failure":
			http.Error(w, "temporary", http.StatusBadGateway)
		case "fallback-model":
			fallbackCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "fallback_ok",
				"choices": []map[string]any{{
					"message":       map[string]any{"role": "assistant", "content": "fallback ok"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		default:
			t.Fatalf("unexpected model %v", body["model"])
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "temporary-failure"},
		{Provider: "mock", Model: "fallback-model"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"can retry"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls=%d, want 1", fallbackCalls.Load())
	}
}

func TestDecodeErrorStillFallsBack(t *testing.T) {
	var fallbackCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch stringValue(body["model"]) {
		case "schema-drift-model":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":`))
		case "fallback-model":
			fallbackCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "decode_fallback_ok",
				"choices": []map[string]any{{
					"message":       map[string]any{"role": "assistant", "content": "fallback ok"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		default:
			t.Fatalf("unexpected model %v", body["model"])
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "schema-drift-model"},
		{Provider: "mock", Model: "fallback-model"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"can decode fallback"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls=%d, want 1", fallbackCalls.Load())
	}
	var attempts []requestAttemptRecord
	if err := svc.usage.db.Order("attempt_index ASC").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].ErrorClass != "decode_error" || !attempts[0].Retryable || attempts[0].FallbackReason != "decode_error" || !attempts[1].Selected {
		t.Fatalf("unexpected attempts: %#v", attempts)
	}
}

func TestSuccessfulUpstreamResponseSizeIsBounded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"huge","choices":[{"message":{"role":"assistant","content":"` + strings.Repeat("x", 512) + `"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Upstream.MaxResponseBytes = 128
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "upstream response exceeded configured size limit") {
		t.Fatalf("body=%s, want size limit error", rr.Body.String())
	}
	var attempts []requestAttemptRecord
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].ErrorClass != "upstream_response_too_large" || attempts[0].Retryable {
		t.Fatalf("unexpected attempts: %#v", attempts)
	}
}

func TestImageURLPrivateDestinationsBlockedBeforeUpstream(t *testing.T) {
	for _, imageURL := range []string{
		"http://127.0.0.1/image.png",
		"http://0.0.0.0/image.png",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/image.png",
		"http://172.16.0.1/image.png",
		"http://192.168.1.10/image.png",
		"http://100.64.0.1/image.png",
		"http://198.18.0.1/image.png",
		"http://192.0.2.1/image.png",
		"http://224.0.0.1/image.png",
		"http://240.0.0.1/image.png",
		"file:///etc/passwd",
		"ftp://example.com/image.png",
	} {
		t.Run(imageURL, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeJSON(w, http.StatusOK, map[string]any{"id": "unexpected"})
			}))
			defer upstream.Close()

			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "vision-model", InputModalities: []string{"text", "image"}}}}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			body := `{"model":"default","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"` + imageURL + `"}}]}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if calls.Load() != 0 {
				t.Fatalf("upstream calls=%d, want 0", calls.Load())
			}
		})
	}
}

func TestImageURLValidationDoesNotDereferenceRemoteImageURL(t *testing.T) {
	var probeCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "vision_public_url",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "vision-model", InputModalities: []string{"text", "image"}}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"https://example.com/redirect-on-get.png"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if probeCalls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want exactly the selected provider call and no router-side image probe", probeCalls.Load())
	}
}

func TestAllowPrivateImageURLsOnlyChangesImageURLAdmission(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "vision_private_url",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Upstream.AllowPrivateImageURLs = true
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "vision-model", InputModalities: []string{"text", "image"}}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"http://127.0.0.1/private.png"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", calls.Load())
	}
}

func TestImageURLPublicAndDataURLsRemainAllowed(t *testing.T) {
	for _, imageURL := range []string{
		"https://93.184.216.34/image.png",
		"data:image/png;base64,AA==",
	} {
		t.Run(imageURL, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeJSON(w, http.StatusOK, map[string]any{
					"id": "vision_ok",
					"choices": []map[string]any{{
						"message":       map[string]any{"role": "assistant", "content": "ok"},
						"finish_reason": "stop",
					}},
					"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
				})
			}))
			defer upstream.Close()

			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "vision-model", InputModalities: []string{"text", "image"}}}}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			body := `{"model":"default","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"` + imageURL + `"}}]}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if calls.Load() != 1 {
				t.Fatalf("upstream calls=%d, want 1", calls.Load())
			}
		})
	}
}

func TestMultipleInlineImagesPersistOnlySafeScalarUsageMetadata(t *testing.T) {
	const rawImageOne = "RAW_OPENAI_IMAGE_DATA"
	const rawImageTwo = "RAW_SECOND_IMAGE_DATA"
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "vision_multi_image",
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     80,
				"completion_tokens": 2,
				"total_tokens":      82,
				"prompt_tokens_details": map[string]any{
					"image_tokens": 32,
				},
			},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "vision-model", InputModalities: []string{"text", "image"}}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{
		"model":"default",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"compare these images"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,` + rawImageOne + `"}},
				{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + rawImageTwo + `"}}
			]
		}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	rawUpstream, _ := json.Marshal(upstreamBody)
	if !strings.Contains(string(rawUpstream), rawImageOne) || !strings.Contains(string(rawUpstream), rawImageTwo) {
		t.Fatalf("upstream did not receive inline image payloads: %s", rawUpstream)
	}

	var rows []usageRecord
	if err := svc.usage.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("usage rows=%d, want 1", len(rows))
	}
	row := rows[0]
	if !row.InputHasImage || row.InputImageCount != 2 || row.InputImageTokens != 32 {
		t.Fatalf("image usage metadata=%#v, want has_image count=2 tokens=32", row)
	}
	var shapeRows []requestShapeRecord
	if err := svc.usage.db.Find(&shapeRows).Error; err != nil {
		t.Fatal(err)
	}
	persisted, err := json.Marshal(struct {
		Usage  []usageRecord
		Shapes []requestShapeRecord
	}{Usage: rows, Shapes: shapeRows})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{rawImageOne, rawImageTwo, "data:image/png", "data:image/jpeg"} {
		if strings.Contains(string(persisted), forbidden) {
			t.Fatalf("usage diagnostics leaked raw image material %q: %s", forbidden, persisted)
		}
	}
}

func TestUpstreamStatusClassifiesQuotaRateBillingFailures(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantClass string
		wantRetry bool
		forbidden []string
	}{
		{
			name:      "openrouter credits exhausted",
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"message":"This request requires more credits than your account balance allows for account acct_live_secret","code":429}}`,
			wantClass: "upstream_quota_exhausted",
			wantRetry: true,
			forbidden: []string{"acct_live_secret", "account balance allows"},
		},
		{
			name:      "insufficient balance",
			status:    http.StatusPaymentRequired,
			body:      `{"error":{"message":"insufficient balance for provider account provider_account_123"}}`,
			wantClass: "upstream_quota_exhausted",
			wantRetry: true,
			forbidden: []string{"provider_account_123", "insufficient balance"},
		},
		{
			name:      "quota exceeded",
			status:    http.StatusForbidden,
			body:      `{"error":{"type":"quota_exceeded","message":"monthly quota exceeded"}}`,
			wantClass: "upstream_quota_exhausted",
			wantRetry: true,
			forbidden: []string{"monthly quota exceeded"},
		},
		{
			name:      "billing disabled",
			status:    http.StatusForbidden,
			body:      `{"error":{"code":"billing_disabled","message":"billing disabled for customer cust_secret"}}`,
			wantClass: "upstream_quota_exhausted",
			wantRetry: true,
			forbidden: []string{"cust_secret", "billing disabled for customer"},
		},
		{
			name:      "ordinary rate limit",
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"message":"rate limit exceeded"}}`,
			wantClass: "upstream_rate_limited",
			wantRetry: true,
		},
		{
			name:      "ordinary bad request",
			status:    http.StatusBadRequest,
			body:      `{"error":{"message":"invalid request"}}`,
			wantClass: "upstream_bad_request",
			wantRetry: false,
		},
		{
			name:      "ordinary auth failure",
			status:    http.StatusUnauthorized,
			body:      `{"error":{"message":"invalid API key"}}`,
			wantClass: "upstream_auth_failed",
			wantRetry: false,
		},
		{
			name:      "model access denied beats generic access marker",
			status:    http.StatusForbidden,
			body:      `{"error":{"code":"model_access_denied","message":"access_denied for requested model"}}`,
			wantClass: "upstream_model_access_denied",
			wantRetry: false,
		},
		{
			name:      "model not enabled beats entitlement marker",
			status:    http.StatusForbidden,
			body:      `{"error":{"code":"model_not_enabled","message":"model_not_enabled for this account"}}`,
			wantClass: "upstream_model_access_denied",
			wantRetry: false,
		},
		{
			name:      "ordinary not found",
			status:    http.StatusNotFound,
			body:      `{"error":{"message":"model not found"}}`,
			wantClass: "upstream_model_access_denied",
			wantRetry: false,
		},
		{
			name:      "ordinary request timeout",
			status:    http.StatusRequestTimeout,
			body:      `{"error":{"message":"request timeout"}}`,
			wantClass: "upstream_timeout",
			wantRetry: true,
		},
		{
			name:      "ordinary upstream 5xx",
			status:    http.StatusBadGateway,
			body:      `{"error":{"message":"temporary outage"}}`,
			wantClass: "upstream_status_5xx",
			wantRetry: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUpstreamStatus(tc.status, []byte(tc.body))
			if got.Class != tc.wantClass || got.Retryable != tc.wantRetry || got.StatusCode != tc.status {
				t.Fatalf("classified=%#v, want class=%s retry=%v status=%d", got, tc.wantClass, tc.wantRetry, tc.status)
			}
			if tc.wantClass == "upstream_quota_exhausted" {
				if !strings.Contains(got.Message, "quota, credits, or billing") {
					t.Fatalf("message=%q, want actionable quota/billing text", got.Message)
				}
				for _, forbidden := range tc.forbidden {
					if strings.Contains(got.Message, forbidden) {
						t.Fatalf("message leaked %q: %s", forbidden, got.Message)
					}
				}
			}
		})
	}
}

func TestUpstreamQuotaExhaustionReturnsSanitizedErrorAcrossSurfaces(t *testing.T) {
	const (
		accountID   = "acct_provider_secret_123"
		providerKey = "sk-provider-secret-1234567890"
		tokenHash   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	cases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"default","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"default","input":"hi","max_output_tokens":16}`,
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"default","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusPaymentRequired, map[string]any{
					"error": map[string]any{
						"message":          "insufficient credits for provider account " + accountID,
						"provider_api_key": providerKey,
						"token_hash":       tokenHash,
						"raw_body":         "caller prompt should not appear",
					},
				})
			}))
			defer upstream.Close()

			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			if tc.name == "responses" {
				cfg.Provider["mock"] = ProviderConfig{BaseURL: upstream.URL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"}
			} else if tc.name == "messages" {
				cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{
					Provider: "mock",
					Model:    "mock-model",
					RequestShapeSupport: RequestShapeSupport{
						SupportedInboundDialects: []string{"anthropic"},
						ValidationStatus:         "passed",
					},
				}}}
			}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			body := rr.Body.String()
			for _, want := range []string{`"type":"upstream-quota-exhausted"`, `"request_id"`, "quota, credits, or billing"} {
				if !strings.Contains(body, want) {
					t.Fatalf("body missing %q: %s", want, body)
				}
			}
			for _, forbidden := range []string{accountID, providerKey, tokenHash, "caller prompt should not appear", "insufficient credits for provider account"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("body leaked %q: %s", forbidden, body)
				}
			}

			var attempts []requestAttemptRecord
			if err := svc.usage.db.Find(&attempts).Error; err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 || attempts[0].ErrorClass != "upstream_quota_exhausted" || !attempts[0].Retryable {
				t.Fatalf("unexpected attempts: %#v", attempts)
			}
			for _, forbidden := range []string{accountID, providerKey, tokenHash, "caller prompt should not appear"} {
				if strings.Contains(attempts[0].ErrorMessage, forbidden) {
					t.Fatalf("attempt leaked %q: %#v", forbidden, attempts[0])
				}
			}
			var errors []requestErrorRecord
			if err := svc.usage.db.Find(&errors).Error; err != nil {
				t.Fatal(err)
			}
			if len(errors) != 1 || errors[0].ErrorType != "upstream-quota-exhausted" || errors[0].Status != http.StatusServiceUnavailable || !errors[0].Retryable {
				t.Fatalf("unexpected error rows: %#v", errors)
			}
		})
	}
}

func TestUpstreamQuotaExhaustionFallsBackAndRecordsAttempt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch stringValue(body["model"]) {
		case "credit-empty":
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": map[string]any{
					"message":    "OpenRouter credits exhausted for account acct_live_secret",
					"account_id": "acct_live_secret",
				},
			})
		case "healthy-model":
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "up_fallback_success",
				"choices": []map[string]any{{
					"message":       map[string]any{"role": "assistant", "content": "fallback ok"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
			})
		default:
			t.Fatalf("unexpected upstream model %v", body["model"])
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{
		{Provider: "mock", Model: "credit-empty"},
		{Provider: "mock", Model: "healthy-model"},
	}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "fallback ok") || strings.Contains(rr.Body.String(), "acct_live_secret") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}

	var attempts []requestAttemptRecord
	if err := svc.usage.db.Order("attempt_index ASC").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempt rows=%d: %#v", len(attempts), attempts)
	}
	if attempts[0].ErrorClass != "upstream_quota_exhausted" || attempts[0].FallbackReason != "upstream_quota_exhausted" || !attempts[0].Retryable || attempts[0].Selected {
		t.Fatalf("unexpected failed attempt: %#v", attempts[0])
	}
	if strings.Contains(attempts[0].ErrorMessage, "acct_live_secret") || strings.Contains(attempts[0].ErrorMessage, "OpenRouter credits exhausted") {
		t.Fatalf("failed attempt leaked upstream body: %#v", attempts[0])
	}
	if !attempts[1].Selected || attempts[1].ErrorClass != "" || attempts[1].Model != "healthy-model" {
		t.Fatalf("unexpected fallback attempt: %#v", attempts[1])
	}
	var rows []usageRecord
	if err := svc.usage.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != http.StatusOK || !rows[0].FallbackUsed || rows[0].Attempts != 2 || rows[0].Error != "" {
		t.Fatalf("unexpected usage rows: %#v", rows)
	}
	if rows[0].TargetModel != "healthy-model" || rows[0].TargetProvider != "mock" {
		t.Fatalf("usage terminal target=%s/%s, want mock/healthy-model after fallback", rows[0].TargetProvider, rows[0].TargetModel)
	}
	var errors []requestErrorRecord
	if err := svc.usage.db.Find(&errors).Error; err != nil {
		t.Fatal(err)
	}
	if len(errors) != 0 {
		t.Fatalf("unexpected error rows for fallback success: %#v", errors)
	}
	var transitions []fallbackTransitionRecord
	if err := svc.usage.db.Find(&transitions).Error; err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 1 {
		t.Fatalf("fallback transition rows=%d: %#v", len(transitions), transitions)
	}
	transition := transitions[0]
	if transition.AttemptIndex != 1 || transition.FailedCandidateIndex != 0 || transition.FallbackCandidateIndex != 1 ||
		transition.FallbackReason != "upstream_quota_exhausted" || !transition.Retryable || !transition.FallbackSucceeded {
		t.Fatalf("unexpected fallback transition: %#v", transition)
	}
}

func TestFallbackAttributesServingTargetPricingCostsCacheAndObservations(t *testing.T) {
	for _, tc := range []struct {
		name        string
		diagnostics *bool
	}{
		{name: "diagnostics-on", diagnostics: boolPtr(true)},
		{name: "diagnostics-off", diagnostics: boolPtr(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				switch stringValue(body["model"]) {
				case "primary-expensive":
					writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": "temporary upstream failure"}})
				case "fallback-cheap":
					writeJSON(w, http.StatusOK, map[string]any{
						"id": "up_fallback_success",
						"choices": []map[string]any{{
							"message":       map[string]any{"role": "assistant", "content": "served by fallback"},
							"finish_reason": "stop",
						}},
						"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 500, "total_tokens": 1500},
					})
				default:
					t.Fatalf("unexpected upstream model %v", body["model"])
				}
			}))
			defer upstream.Close()

			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			cfg.Server.Diagnostics.Enabled = tc.diagnostics
			cfg.Server.Cache.DefaultTTL = time.Minute
			cfg.Models["default"] = ModelGroup{
				Strategy: "failover",
				Targets: []Target{
					{
						Provider:                 "mock",
						Model:                    "primary-expensive",
						InputPricePerMillionUSD:  1.0,
						OutputPricePerMillionUSD: 2.0,
						PricingSource:            "test-primary",
						PricingUpdatedAt:         "2026-09-08",
					},
					{
						Provider:                 "mock",
						Model:                    "fallback-cheap",
						InputPricePerMillionUSD:  0.10,
						OutputPricePerMillionUSD: 0.40,
						PricingSource:            "test-fallback",
						PricingUpdatedAt:         "2026-09-08",
					},
				},
			}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			reqBody := `{"model":"default","temperature":0,"messages":[{"role":"user","content":"attribute fallback"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}

			var rows []usageRecord
			if err := svc.usage.db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("usage rows=%d: %#v", len(rows), rows)
			}
			row := rows[0]
			if row.TargetModel != "fallback-cheap" || row.TargetProvider != "mock" {
				t.Fatalf("terminal target=%s/%s, want mock/fallback-cheap", row.TargetProvider, row.TargetModel)
			}
			wantCost := roundUSD(1000*0.10/1_000_000 + 500*0.40/1_000_000)
			if row.TotalCostUSD != wantCost {
				t.Fatalf("total_cost_usd=%v, want serving-target cost %v (primary would be %v)", row.TotalCostUSD, wantCost, roundUSD(1000*1.0/1_000_000+500*2.0/1_000_000))
			}
			if row.PricingSource != "test-fallback" {
				t.Fatalf("pricing_source=%q, want test-fallback", row.PricingSource)
			}

			primaryStats := svc.observations.stats(dynamicObservationKey("default", "mock", "primary-expensive"), defaultDynamicObservationWindow)
			fallbackStats := svc.observations.stats(dynamicObservationKey("default", "mock", "fallback-cheap"), defaultDynamicObservationWindow)
			if primaryStats.Count != 1 || primaryStats.ErrorRate != 1 {
				t.Fatalf("primary observation stats=%#v, want one failed observation even with diagnostics=%v", primaryStats, *tc.diagnostics)
			}
			if fallbackStats.Count != 1 || fallbackStats.ErrorRate != 0 {
				t.Fatalf("fallback observation stats=%#v, want one success observation even with diagnostics=%v", fallbackStats, *tc.diagnostics)
			}

			decoded, err := decodeRequest("openai-chat", []byte(reqBody), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			primaryKey := cacheKey(decoded, Target{Provider: "mock", Model: "primary-expensive"}, "alice", "metrum-insights")
			fallbackKey := cacheKey(decoded, Target{Provider: "mock", Model: "fallback-cheap"}, "alice", "metrum-insights")
			if _, ok := svc.cache.Get(primaryKey); ok {
				t.Fatal("fallback response stored under failed primary cache key")
			}
			if cached, ok := svc.cache.Get(fallbackKey); !ok || cached == nil || cached.Text != "served by fallback" {
				t.Fatalf("fallback response missing under serving-target cache key: ok=%v cached=%#v", ok, cached)
			}
		})
	}
}

func TestUpstreamFailureResponseDoesNotLabelMixedAttemptsAsQuota(t *testing.T) {
	rc := &requestContext{rec: logRecord{AttemptsDetail: []attemptLogRecord{
		{ErrorClass: "upstream_timeout"},
		{ErrorClass: "upstream_quota_exhausted"},
	}}}
	code, status := upstreamFailureResponse(upstreamError{Class: "upstream_quota_exhausted"}, rc)
	if code != "upstream-failed" || status != http.StatusBadGateway {
		t.Fatalf("mixed attempts response code=%s status=%d", code, status)
	}

	rc = &requestContext{rec: logRecord{AttemptsDetail: []attemptLogRecord{
		{ErrorClass: "upstream_quota_exhausted"},
		{ErrorClass: "upstream_quota_exhausted"},
	}}}
	code, status = upstreamFailureResponse(upstreamError{Class: "upstream_quota_exhausted"}, rc)
	if code != "upstream-quota-exhausted" || status != http.StatusServiceUnavailable {
		t.Fatalf("quota attempts response code=%s status=%d", code, status)
	}
}

func TestDiagnosticsSanitizeUpstreamErrorBeforePersistence(t *testing.T) {
	const (
		echoedPrompt = "prompt-like user text: summarize confidential launch notes"
		bearerToken  = "Bearer fake-provider-bearer-token-1234567890"
		providerKey  = "sk-fake-provider-key-1234567890"
		tokenHash    = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		nestedDetail = "nested upstream body detail"
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]any{
				"message": "provider echoed request context",
				"details": map[string]any{
					"prompt":           echoedPrompt,
					"authorization":    bearerToken,
					"provider_api_key": providerKey,
					"token_hash":       tokenHash,
					"nested": map[string]any{
						"body": nestedDetail,
					},
				},
			},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Diagnostics.StoreSanitizedUpstreamError = boolPtr(true)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"please do not persist this prompt"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var attempts []requestAttemptRecord
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	var traces []requestTraceEventRecord
	if err := svc.usage.db.Find(&traces).Error; err != nil {
		t.Fatal(err)
	}
	var errors []requestErrorRecord
	if err := svc.usage.db.Find(&errors).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || len(errors) != 1 {
		t.Fatalf("diagnostic rows attempts=%d errors=%d", len(attempts), len(errors))
	}
	foundFailedTrace := false
	for _, trace := range traces {
		if trace.Event == "upstream_attempt_failed" {
			foundFailedTrace = true
			if !strings.Contains(trace.Message, "upstream error body redacted") {
				t.Fatalf("failed trace message was not redacted: %q", trace.Message)
			}
		}
	}
	if !foundFailedTrace {
		t.Fatalf("upstream_attempt_failed trace not found: %#v", traces)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsText := string(raw)
	for _, attempt := range attempts {
		diagnosticsText += "\n" + attempt.ErrorMessage
	}
	for _, trace := range traces {
		diagnosticsText += "\n" + trace.Message
	}
	for _, rec := range errors {
		diagnosticsText += "\n" + rec.ErrorMessage
	}
	for _, forbidden := range []string{echoedPrompt, bearerToken, providerKey, tokenHash, nestedDetail, "please do not persist this prompt"} {
		if strings.Contains(diagnosticsText, forbidden) {
			t.Fatalf("diagnostics leaked %q in: %s", forbidden, diagnosticsText)
		}
	}
	for _, want := range []string{`"trace_events"`, `"attempts_detail"`, "upstream error body redacted"} {
		if !strings.Contains(diagnosticsText, want) {
			t.Fatalf("sanitized diagnostics missing %q in: %s", want, diagnosticsText)
		}
	}
}

func TestDiagnosticsSanitizeTruncatedUpstreamErrorBeforePersistence(t *testing.T) {
	const (
		echoedPrompt = "truncated prompt-like user text"
		bearerToken  = "Bearer truncated-provider-bearer-token-1234567890"
		providerKey  = "sk-truncated-provider-key-1234567890"
		tokenHash    = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	bodyPrefix := `{"error":{"message":"provider echoed request context","prompt":"` + echoedPrompt + `","authorization":"` + bearerToken + `","token_hash":"` + tokenHash + `","provider_api_key":"` + providerKey + `","tail":"`
	upstreamBody := bodyPrefix + strings.Repeat("x", 2048)
	maxErrorBytes := len(bodyPrefix) + 32
	if json.Valid([]byte(upstreamBody[:maxErrorBytes])) {
		t.Fatal("truncated upstream body unexpectedly valid JSON")
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Diagnostics.StoreSanitizedUpstreamError = boolPtr(true)
	cfg.Server.Diagnostics.MaxErrorBytes = maxErrorBytes
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"do not persist caller prompt"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var attempts []requestAttemptRecord
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	var traces []requestTraceEventRecord
	if err := svc.usage.db.Find(&traces).Error; err != nil {
		t.Fatal(err)
	}
	var errors []requestErrorRecord
	if err := svc.usage.db.Find(&errors).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || len(errors) != 1 {
		t.Fatalf("diagnostic rows attempts=%d errors=%d", len(attempts), len(errors))
	}

	raw, err := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsText := string(raw)
	for _, attempt := range attempts {
		diagnosticsText += "\n" + attempt.ErrorMessage
	}
	for _, trace := range traces {
		diagnosticsText += "\n" + trace.Message
	}
	for _, rec := range errors {
		diagnosticsText += "\n" + rec.ErrorMessage
	}
	for _, forbidden := range []string{echoedPrompt, bearerToken, providerKey, tokenHash, "do not persist caller prompt"} {
		if strings.Contains(diagnosticsText, forbidden) {
			t.Fatalf("diagnostics leaked %q in: %s", forbidden, diagnosticsText)
		}
	}
	if !strings.Contains(diagnosticsText, "upstream error body redacted") {
		t.Fatalf("sanitized diagnostics missing upstream body redaction marker in: %s", diagnosticsText)
	}
	for _, field := range []string{`"prompt"`, `"authorization"`, `"token_hash"`, `"provider_api_key"`} {
		if strings.Contains(diagnosticsText, field) {
			t.Fatalf("sanitized diagnostics retained upstream body field %q in: %s", field, diagnosticsText)
		}
	}
}

func TestSanitizedUpstreamErrorDetailsPersistAllowlistedFields(t *testing.T) {
	const (
		echoedPrompt = "customer prompt should never be stored"
		bearerToken  = "Bearer detail-provider-token-1234567890"
		providerKey  = "sk-detail-provider-key-1234567890"
	)

	scenarios := []struct {
		name     string
		body     map[string]any
		expected map[string]string
	}{
		{
			name: "openai-compatible",
			body: map[string]any{
				"error": map[string]any{
					"message": "Unsupported value for parameter temperature",
					"type":    "invalid_request_error",
					"code":    "unsupported_value",
					"param":   "temperature",
					"details": map[string]any{
						"prompt":           echoedPrompt,
						"authorization":    bearerToken,
						"provider_api_key": providerKey,
					},
				},
				"request_id": "req_provider_safe_123",
			},
			expected: map[string]string{
				"code":       "unsupported_value",
				"message":    "provider_message:unsupported_field",
				"param":      "temperature",
				"request_id": "req_provider_safe_123",
				"type":       "invalid_request_error",
			},
		},
		{
			name: "anthropic-compatible",
			body: map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "tool schema invalid for prompt " + echoedPrompt,
				},
				"request_id": "req_anthropic_safe_123",
				"body": map[string]any{
					"messages": []string{echoedPrompt},
					"token":    bearerToken,
				},
			},
			expected: map[string]string{
				"message":    "provider_message:tool_schema_rejected",
				"request_id": "req_anthropic_safe_123",
				"type":       "invalid_request_error",
			},
		},
		{
			name: "generic",
			body: map[string]any{
				"error_code":          "context_length_exceeded",
				"message":             "maximum context length exceeded near " + echoedPrompt,
				"provider_request_id": "prq_safe_123",
				"status":              400,
				"debug": map[string]any{
					"prompt":           echoedPrompt,
					"authorization":    bearerToken,
					"provider_api_key": providerKey,
				},
			},
			expected: map[string]string{
				"error_code":          "context_length_exceeded",
				"message":             "provider_message:context_limit",
				"provider_request_id": "prq_safe_123",
				"status":              "400",
			},
		},
		{
			name: "openrouter-style",
			body: map[string]any{
				"error": map[string]any{
					"message": "upstream provider maximum context length exceeded near " + echoedPrompt,
					"code":    "context_length_exceeded",
					"metadata": map[string]any{
						"raw":   `{"prompt":"` + echoedPrompt + `"}`,
						"token": bearerToken,
					},
				},
				"provider_request_id": "or_req_safe_123",
			},
			expected: map[string]string{
				"code":                "context_length_exceeded",
				"message":             "provider_message:context_limit",
				"provider_request_id": "or_req_safe_123",
			},
		},
		{
			name: "baseten-style",
			body: map[string]any{
				"error": map[string]any{
					"message": "Unknown field store in request body",
					"type":    "invalid_request_error",
					"code":    "unsupported_value",
					"param":   "store",
				},
				"request_id": "bt_req_safe_123",
				"request": map[string]any{
					"messages": []string{echoedPrompt},
					"headers":  map[string]any{"authorization": bearerToken},
				},
			},
			expected: map[string]string{
				"code":       "unsupported_value",
				"message":    "provider_message:unsupported_field",
				"param":      "store",
				"request_id": "bt_req_safe_123",
				"type":       "invalid_request_error",
			},
		},
		{
			name: "minimax-style",
			body: map[string]any{
				"error_code": "unsupported_image",
				"error": map[string]any{
					"message": "unsupported image input for this model",
					"type":    "invalid_request_error",
				},
				"request_id": "mm_req_safe_123",
				"input": map[string]any{
					"prompt": echoedPrompt,
					"tools":  []string{"raw tool schema should not be stored"},
				},
			},
			expected: map[string]string{
				"error_code": "unsupported_image",
				"message":    "provider_message:unsupported_field",
				"request_id": "mm_req_safe_123",
				"type":       "invalid_request_error",
			},
		},
		{
			name: "kimi-style",
			body: map[string]any{
				"error": map[string]any{
					"message": "model not found or access denied",
					"type":    "invalid_request_error",
					"code":    "model_not_found",
				},
				"request_id": "moonshot_req_safe_123",
				"messages":   []string{echoedPrompt},
			},
			expected: map[string]string{
				"code":       "model_not_found",
				"message":    "provider_message:model_access",
				"request_id": "moonshot_req_safe_123",
				"type":       "invalid_request_error",
			},
		},
		{
			name: "xai-style",
			body: map[string]any{
				"error": map[string]any{
					"message": "permission denied for API key",
					"type":    "authentication_error",
					"code":    "invalid_api_key",
				},
				"request_id": "xai_req_safe_123",
				"debug": map[string]any{
					"authorization": bearerToken,
					"raw_body":      echoedPrompt,
				},
			},
			expected: map[string]string{
				"code":       "invalid_api_key",
				"message":    "provider_message:auth",
				"request_id": "xai_req_safe_123",
				"type":       "authentication_error",
			},
		},
		{
			name: "crusoe-style",
			body: map[string]any{
				"error": map[string]any{
					"message": "tool schema invalid for selected parser",
					"type":    "invalid_request_error",
					"code":    "invalid_tool_schema",
					"param":   "tools.0.function.parameters",
				},
				"request_id": "crusoe_req_safe_123",
				"tools": []map[string]any{{
					"function": map[string]any{"parameters": echoedPrompt},
				}},
			},
			expected: map[string]string{
				"code":       "invalid_tool_schema",
				"message":    "provider_message:tool_schema_rejected",
				"param":      "tools.0.function.parameters",
				"request_id": "crusoe_req_safe_123",
				"type":       "invalid_request_error",
			},
		},
		{
			name: "unsafe-param-redacted",
			body: map[string]any{
				"error": map[string]any{
					"message": "Bad request",
					"type":    "invalid_request_error",
					"code":    "invalid_param",
					"param":   "messages.0.content " + echoedPrompt,
				},
			},
			expected: map[string]string{
				"code":    "invalid_param",
				"message": "provider_message:invalid_request",
				"param":   "[REDACTED]",
				"type":    "invalid_request_error",
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(scenario.body)
			}))
			defer upstream.Close()

			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"caller prompt should not be stored"}]}`))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}

			var attempts []requestAttemptRecord
			if err := svc.usage.db.Find(&attempts).Error; err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 || attempts[0].ErrorClass != "upstream_bad_request" || attempts[0].Retryable {
				t.Fatalf("unexpected attempt: %#v", attempts)
			}
			var details []requestUpstreamErrorDetailRecord
			if err := svc.usage.db.Order("field_name").Find(&details).Error; err != nil {
				t.Fatal(err)
			}
			if len(details) == 0 {
				t.Fatal("expected sanitized upstream error detail rows")
			}
			got := map[string]string{}
			allText := ""
			for _, detail := range details {
				got[detail.FieldName] = detail.FieldValue
				allText += detail.FieldName + "=" + detail.FieldValue + " source=" + detail.Source + "\n"
				if detail.RequestID == "" || detail.AttemptIndex != 1 || detail.StatusCode != http.StatusBadRequest || detail.ErrorClass != "upstream_bad_request" {
					t.Fatalf("bad detail row: %#v", detail)
				}
			}
			for key, want := range scenario.expected {
				if got[key] != want {
					t.Fatalf("detail %s=%q, want %q; all details:\n%s", key, got[key], want, allText)
				}
			}
			for _, forbidden := range []string{echoedPrompt, bearerToken, providerKey, "caller prompt should not be stored", "Unsupported value for parameter temperature", "authorization", "provider_api_key"} {
				if strings.Contains(allText, forbidden) {
					t.Fatalf("upstream error details leaked %q in:\n%s", forbidden, allText)
				}
			}
		})
	}
}

func TestSanitizedUpstreamErrorDetailsExplicitOptOut(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "unsupported field store",
				"type":    "invalid_request_error",
				"code":    "unsupported_value",
			},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.Diagnostics.StoreSanitizedUpstreamError = boolPtr(false)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"caller prompt should not be stored"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var count int64
	if err := svc.usage.db.Model(&requestUpstreamErrorDetailRecord{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("upstream error details count=%d, want explicit opt-out to suppress rows", count)
	}
}

func TestConfiguredDefaultModelGroupHandlesOmittedModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_default",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "configured default ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		})
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.DefaultModelGroup = "example-basic"
	cfg.Models = map[string]ModelGroup{
		"example-basic": {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model"}}},
	}
	cfg.Callers[0].Allow = []string{"example-basic"}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "configured default ok") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

func TestOmittedModelWithoutConfiguredDefaultReturnsMissingModel(t *testing.T) {
	svc := newTestService(t, "http://127.0.0.1:1", "provider-key")
	defer svc.Close()
	svc.cfg.Server.DefaultModelGroup = ""

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "missing-model") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

func testContentCaptureEncryption(t *testing.T) ContentCaptureEncryptionConfig {
	t.Helper()
	t.Setenv("CONTENT_CAPTURE_LOCAL_KEY", strings.Repeat("11", 32))
	return ContentCaptureEncryptionConfig{Enabled: true, LocalKeyID: "test-content-capture-key"}
}

func testContentCapturePlaintext(t *testing.T, ciphertext, nonce, localKeyID string) string {
	t.Helper()
	key, err := hex.DecodeString(strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := decryptContentCaptureValue(key, localKeyID, ciphertext, nonce)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func TestContentCaptureEnabledRequiresKeyMaterialAtStartup(t *testing.T) {
	t.Setenv("CONTENT_CAPTURE_LOCAL_KEY", "")
	t.Setenv("CONTENT_CAPTURE_KMS_KEY", "")
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.ContentCapture = ContentCaptureConfig{
		Enabled:        true,
		CaptureRequest: true,
		Encryption:     ContentCaptureEncryptionConfig{Enabled: true, LocalKeyID: "missing-test-key"},
	}
	if svc, err := New(cfg); err == nil {
		svc.Close()
		t.Fatal("New() accepted enabled content capture without key material")
	} else if !strings.Contains(err.Error(), "CONTENT_CAPTURE_LOCAL_KEY is required") {
		t.Fatalf("New() error=%v", err)
	}
}

func TestContentCaptureDisabledByDefault(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_default_capture_disabled",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "metadata only"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		})
	}))
	defer upstream.Close()
	svc := newTestService(t, upstream.URL, "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"do not capture this by default"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var count int64
	if err := svc.usage.db.Model(&contentCaptureRecord{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("content capture rows=%d, want 0", count)
	}
}

func TestContentCaptureRequestPayloadDropsRawForScopeOptOuts(t *testing.T) {
	req := &IRRequest{
		Model: "default",
		Messages: []IRMessage{{
			Role: "user",
			Parts: []IRContentPart{{
				Type:     "image",
				ImageURL: "data:image/png;base64,RAW_NORMALIZED_IMAGE_DATA",
			}},
		}},
		Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "raw_tool"}}},
		Raw: map[string]any{
			"tools": []any{map[string]any{"function": map[string]any{"name": "raw_tool"}}},
			"messages": []any{map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,RAW_OPENAI_IMAGE_DATA"}},
					map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "RAW_ANTHROPIC_IMAGE_DATA"}},
				},
			}},
		},
	}
	payload := contentCaptureRequestPayload(req, ContentCaptureConfig{CaptureImages: false, CaptureToolCalls: false})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"\"raw\"", "\"tools\"", "raw_tool", "RAW_NORMALIZED_IMAGE_DATA", "RAW_OPENAI_IMAGE_DATA", "RAW_ANTHROPIC_IMAGE_DATA"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("request capture payload leaked %q in %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "[IMAGE_REDACTED]") {
		t.Fatalf("request capture payload did not retain image redaction marker: %s", text)
	}
}

func TestContentCaptureStoresRedactedRequestResponseAndAllowedHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_capture_enabled",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "send result to bob@example.com with sk-test-secret"},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14},
		})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.ContentCapture = ContentCaptureConfig{
		Enabled:                 true,
		RetentionDays:           7,
		CaptureRequest:          true,
		CaptureResponse:         true,
		CaptureHeadersAllowlist: []string{"User-Agent", "X-Trace-Id"},
		RedactionPatterns:       []ContentCaptureRedactionRule{{Name: "email", Expression: `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`}},
		Encryption:              testContentCaptureEncryption(t),
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"email alice@example.com and use Bearer rtr_should_not_store_secret"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("User-Agent", "capture-test")
	req.Header.Set("X-Trace-Id", "trace-123")
	req.Header.Set("X-API-Key", "do-not-store")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var rows []contentCaptureRecord
	if err := svc.usage.db.Order("scope").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("capture rows=%d, want 2: %#v", len(rows), rows)
	}
	if !rows[0].Encrypted || !rows[1].Encrypted {
		t.Fatalf("capture rows were not marked encrypted: %#v", rows)
	}
	joinedCiphertext := rows[0].ContentText + "\n" + rows[1].ContentText
	joined := testContentCapturePlaintext(t, rows[0].ContentText, rows[0].EncryptionNonce, rows[0].EncryptionLocalKeyID) +
		"\n" + testContentCapturePlaintext(t, rows[1].ContentText, rows[1].EncryptionNonce, rows[1].EncryptionLocalKeyID)
	for _, forbidden := range []string{testToken, cfg.Callers[0].TokenSHA256, "alice@example.com", "bob@example.com", "sk-test-secret", "rtr_should_not_store_secret", "do-not-store"} {
		if strings.Contains(joinedCiphertext, forbidden) || strings.Contains(joined, forbidden) {
			t.Fatalf("captured content leaked %q", forbidden)
		}
	}
	for _, want := range []string{"[REDACTED_EMAIL]", "[REDACTED_SECRET]"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("captured content missing redaction marker %q in %s", want, joined)
		}
	}
	for _, row := range rows {
		if row.RequestID == "" || row.CallerID != "alice" || row.TokenID != "rtr_alice_test" {
			t.Fatalf("capture row not joinable/safe: %#v", row)
		}
		if row.RetentionUntil == "" {
			t.Fatalf("capture row missing retention_until: %#v", row)
		}
	}
	var headers []contentCaptureHeaderRecord
	if err := svc.usage.db.Order("name").Find(&headers).Error; err != nil {
		t.Fatal(err)
	}
	if len(headers) != 2 {
		t.Fatalf("header rows=%d, want 2: %#v", len(headers), headers)
	}
	headerText := ""
	for _, h := range headers {
		if !h.Encrypted {
			t.Fatalf("header was not encrypted: %#v", h)
		}
		headerText += h.Name + "=" + testContentCapturePlaintext(t, h.Value, h.EncryptionNonce, h.EncryptionLocalKeyID) + "\n"
	}
	if !strings.Contains(headerText, "User-Agent=capture-test") || !strings.Contains(headerText, "X-Trace-Id=trace-123") {
		t.Fatalf("allowed headers not captured: %s", headerText)
	}
	if strings.Contains(headerText, "Authorization") || strings.Contains(headerText, "X-Api-Key") {
		t.Fatalf("forbidden header captured: %s", headerText)
	}
}

func TestContentCaptureStoresSanitizedUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"prompt: secret body Authorization: Bearer sk-leaky-secret"}}`))
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.ContentCapture = ContentCaptureConfig{
		Enabled:               true,
		RetentionDays:         7,
		CaptureUpstreamErrors: true,
		Encryption:            testContentCaptureEncryption(t),
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"trigger upstream failure"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var row contentCaptureRecord
	if err := svc.usage.db.Where("scope = ?", contentCaptureScopeUpstreamError).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.SourceStatus != http.StatusBadGateway {
		t.Fatalf("source status=%d", row.SourceStatus)
	}
	plaintext := testContentCapturePlaintext(t, row.ContentText, row.EncryptionNonce, row.EncryptionLocalKeyID)
	for _, forbidden := range []string{"sk-leaky-secret", "secret body"} {
		if strings.Contains(row.ContentText, forbidden) || strings.Contains(plaintext, forbidden) {
			t.Fatalf("upstream error capture leaked %q in %s", forbidden, row.ContentText)
		}
	}
	if !strings.Contains(plaintext, "upstream error body redacted") {
		t.Fatalf("upstream error capture missing sanitized marker: %s", plaintext)
	}
}

func TestContentCaptureAdminDeleteRequiresContentAdminAndAudits(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_capture_delete",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "captured"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	adminToken := "rtr_content_admin_test_token"
	adminHash := sha256.Sum256([]byte(adminToken))
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, RetentionDays: 7, CaptureRequest: true, Encryption: testContentCaptureEncryption(t)}
	cfg.Callers = append(cfg.Callers, CallerConfig{
		ID:           "content-admin",
		User:         "content-admin",
		Project:      "metrum-insights",
		Environment:  "test",
		TokenSHA256:  hex.EncodeToString(adminHash[:]),
		TokenID:      "rtr_content_admin_test",
		Allow:        []string{"default"},
		ContentAdmin: true,
		Rate:         RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
		Quota:        QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
		Key:          KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
	})
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"capture me"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	requestID := rr.Header().Get("X-Request-Id")
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/"+requestID, nil)
	deleteReq.Header.Set("Authorization", "Bearer "+testToken)
	deleteRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusForbidden {
		t.Fatalf("non-admin delete status=%d body=%s", deleteRR.Code, deleteRR.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/"+requestID, nil)
	adminReq.Header.Set("Authorization", "Bearer "+adminToken)
	adminRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(adminRR, adminReq)
	if adminRR.Code != http.StatusOK {
		t.Fatalf("admin delete status=%d body=%s", adminRR.Code, adminRR.Body.String())
	}
	var remaining int64
	if err := svc.usage.db.Model(&contentCaptureRecord{}).Where("request_id = ?", requestID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("remaining captures=%d, want 0", remaining)
	}
	var audit contentCaptureAuditRecord
	if err := svc.usage.db.Where("action = ? AND request_id = ?", "request_delete", requestID).First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.ActorCallerID != "content-admin" || audit.ActorTokenID != "rtr_content_admin_test" || audit.RowsAffected != 1 {
		t.Fatalf("unexpected audit row: %#v", audit)
	}
}

func TestContentCaptureAdminDeleteRequiresTargetDomainAuthorization(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_capture_cross_domain_delete",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "captured"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	adminToken := "rtr_content_admin_cross_domain_test_token"
	adminHash := sha256.Sum256([]byte(adminToken))
	otherToken := "rtr_other_capture_owner_test_token"
	otherHash := sha256.Sum256([]byte(otherToken))
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, RetentionDays: 7, CaptureRequest: true, Encryption: testContentCaptureEncryption(t)}
	cfg.Callers = append(cfg.Callers,
		CallerConfig{
			ID:           "content-admin",
			User:         "content-admin",
			Project:      "platform",
			Environment:  "test",
			TokenSHA256:  hex.EncodeToString(adminHash[:]),
			TokenID:      "rtr_content_admin_cross_domain_test",
			Allow:        []string{"default"},
			ContentAdmin: true,
			Rate:         RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:        QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:          KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		},
		CallerConfig{
			ID:          "other-capture-owner",
			User:        "other-capture-owner",
			Project:     "other",
			Environment: "test",
			TokenSHA256: hex.EncodeToString(otherHash[:]),
			TokenID:     "rtr_other_capture_owner_test",
			Allow:       []string{"default"},
			Rate:        RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:         KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		},
	)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"capture other domain"}]}`))
	req.Header.Set("Authorization", "Bearer "+otherToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	requestID := rr.Header().Get("X-Request-Id")

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/"+requestID, nil)
	deleteReq.Header.Set("Authorization", "Bearer "+adminToken)
	deleteRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusForbidden || !strings.Contains(deleteRR.Body.String(), "content-forbidden") {
		t.Fatalf("cross-domain delete status=%d body=%s", deleteRR.Code, deleteRR.Body.String())
	}
	var remaining int64
	if err := svc.usage.db.Model(&contentCaptureRecord{}).Where("request_id = ?", requestID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("remaining captures=%d, want 1", remaining)
	}
}

func TestContentCaptureAdminDeleteAllowsTargetDomainOnlyGrant(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_capture_target_domain_only",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "captured"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	centralToken := "rtr_central_content_admin_test_token"
	centralHash := sha256.Sum256([]byte(centralToken))
	ownerToken := "rtr_target_domain_owner_test_token"
	ownerHash := sha256.Sum256([]byte(ownerToken))
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, RetentionDays: 7, CaptureRequest: true, Encryption: testContentCaptureEncryption(t)}
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Policy: []string{
			"g, caller:central-content-admin, content_admin, other/test",
			"p, content_admin, other/test, content:capture, delete",
		},
	}
	cfg.Callers = append(cfg.Callers,
		CallerConfig{
			ID:          "central-content-admin",
			User:        "central-content-admin",
			Project:     "platform",
			Environment: "ops",
			TokenSHA256: hex.EncodeToString(centralHash[:]),
			TokenID:     "rtr_central_content_admin_test",
			Allow:       []string{"default"},
			Rate:        RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:         KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		},
		CallerConfig{
			ID:          "target-domain-owner",
			User:        "target-domain-owner",
			Project:     "other",
			Environment: "test",
			TokenSHA256: hex.EncodeToString(ownerHash[:]),
			TokenID:     "rtr_target_domain_owner_test",
			Allow:       []string{"default"},
			Rate:        RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:         KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		},
	)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"capture target domain"}]}`))
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/"+rr.Header().Get("X-Request-Id"), nil)
	deleteReq.Header.Set("Authorization", "Bearer "+centralToken)
	deleteRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("target-domain-only delete status=%d body=%s", deleteRR.Code, deleteRR.Body.String())
	}
}

func TestContentCaptureAdminDeleteUsesCaptureTimeDomain(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_capture_time_domain",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "captured"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	oldAdminToken := "rtr_old_domain_content_admin_test_token"
	oldAdminHash := sha256.Sum256([]byte(oldAdminToken))
	newAdminToken := "rtr_new_domain_content_admin_test_token"
	newAdminHash := sha256.Sum256([]byte(newAdminToken))
	ownerToken := "rtr_movable_capture_owner_test_token"
	ownerHash := sha256.Sum256([]byte(ownerToken))
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.ContentCapture = ContentCaptureConfig{Enabled: true, RetentionDays: 7, CaptureRequest: true, Encryption: testContentCaptureEncryption(t)}
	cfg.Callers = append(cfg.Callers,
		CallerConfig{
			ID:           "old-domain-admin",
			User:         "old-domain-admin",
			Project:      "other",
			Environment:  "test",
			TokenSHA256:  hex.EncodeToString(oldAdminHash[:]),
			TokenID:      "rtr_old_domain_content_admin_test",
			Allow:        []string{"default"},
			ContentAdmin: true,
			Rate:         RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:        QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:          KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		},
		CallerConfig{
			ID:           "new-domain-admin",
			User:         "new-domain-admin",
			Project:      "moved",
			Environment:  "test",
			TokenSHA256:  hex.EncodeToString(newAdminHash[:]),
			TokenID:      "rtr_new_domain_content_admin_test",
			Allow:        []string{"default"},
			ContentAdmin: true,
			Rate:         RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:        QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:          KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		},
		CallerConfig{
			ID:          "movable-capture-owner",
			User:        "movable-capture-owner",
			Project:     "other",
			Environment: "test",
			TokenSHA256: hex.EncodeToString(ownerHash[:]),
			TokenID:     "rtr_movable_capture_owner_test",
			Allow:       []string{"default"},
			Rate:        RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:         KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		},
	)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"capture old domain"}]}`))
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	requestID := rr.Header().Get("X-Request-Id")
	svc.quota.callers["movable-capture-owner"].project = "moved"
	svc.quota.callers["movable-capture-owner"].cfg.Project = "moved"

	newReq := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/"+requestID, nil)
	newReq.Header.Set("Authorization", "Bearer "+newAdminToken)
	newRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(newRR, newReq)
	if newRR.Code != http.StatusForbidden || !strings.Contains(newRR.Body.String(), "content-forbidden") {
		t.Fatalf("new-domain delete status=%d body=%s", newRR.Code, newRR.Body.String())
	}

	oldReq := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/"+requestID, nil)
	oldReq.Header.Set("Authorization", "Bearer "+oldAdminToken)
	oldRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(oldRR, oldReq)
	if oldRR.Code != http.StatusOK {
		t.Fatalf("old-domain delete status=%d body=%s", oldRR.Code, oldRR.Body.String())
	}
}

func TestContentCaptureMaintenanceUsesCasbinAuthorization(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Policy: []string{
			"g, caller:content-policy, content_admin, platform/prod",
			"g, caller:reports-only, reports_admin, platform/prod",
			"g, caller:content-wrong-domain, content_admin, platform/prod",
			"p, content_admin, platform/prod, content:capture, delete|purge",
			"p, reports_admin, platform/prod, admin:reports, read|export|drilldown",
		},
	}
	contentToken := "rtr_content_policy_test_token"
	reportsToken := "rtr_reports_only_test_token"
	wrongDomainToken := "rtr_content_wrong_domain_test_token"
	for _, caller := range []struct {
		id, token, project, environment string
	}{
		{id: "content-policy", token: contentToken, project: "platform", environment: "prod"},
		{id: "reports-only", token: reportsToken, project: "platform", environment: "prod"},
		{id: "content-wrong-domain", token: wrongDomainToken, project: "platform", environment: "test"},
	} {
		sum := sha256.Sum256([]byte(caller.token))
		cfg.Callers = append(cfg.Callers, CallerConfig{
			ID:          caller.id,
			User:        caller.id,
			Project:     caller.project,
			Environment: caller.environment,
			TokenSHA256: hex.EncodeToString(sum[:]),
			TokenID:     caller.id,
			Allow:       []string{"default"},
			Rate:        RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:         KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		})
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	unauth := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/req_unauth", nil)
	unauthRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(unauthRR, unauth)
	if unauthRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauth content delete status=%d body=%s", unauthRR.Code, unauthRR.Body.String())
	}

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "ordinary caller", token: testToken},
		{name: "reports only", token: reportsToken},
		{name: "wrong domain", token: wrongDomainToken},
	} {
		req := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/req_denied", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		rr := httptest.NewRecorder()
		svc.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "content-forbidden") {
			t.Fatalf("%s content delete status=%d body=%s", tc.name, rr.Code, rr.Body.String())
		}
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/content-captures/req_allowed", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+contentToken)
	deleteRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("policy content delete status=%d body=%s", deleteRR.Code, deleteRR.Body.String())
	}

	purgeReq := httptest.NewRequest(http.MethodPost, "/v1/content-captures/purge-expired", nil)
	purgeReq.Header.Set("Authorization", "Bearer "+contentToken)
	purgeRR := httptest.NewRecorder()
	svc.Handler().ServeHTTP(purgeRR, purgeReq)
	if purgeRR.Code != http.StatusOK {
		t.Fatalf("policy content purge status=%d body=%s", purgeRR.Code, purgeRR.Body.String())
	}
}

func TestMalformedCasbinPolicyFailsStartup(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Server.AdminAuth.Authorization = AdminAuthorizationConfig{
		Enabled: true,
		Policy:  []string{"p, content_admin, platform/prod, content:capture"},
	}
	svc, err := New(cfg)
	if err == nil {
		svc.Close()
		t.Fatal("New succeeded with malformed authorization policy")
	}
	if !strings.Contains(err.Error(), "p lines require subject, domain, object, action") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpstreamAttemptTimeoutReturnsGatewayTimeoutAndDiagnostics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "late",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "late"},
			}},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	group := cfg.Models["default"]
	group.AttemptTimeoutMS = 10
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"type":"upstream-timeout"`) ||
		!strings.Contains(rr.Body.String(), `"request_id"`) {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
	var attempts []requestAttemptRecord
	if err := svc.usage.db.Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempt rows=%d", len(attempts))
	}
	if !attempts[0].TimedOut || attempts[0].ErrorClass != "upstream_timeout" || attempts[0].AttemptTimeoutMS != 10 {
		t.Fatalf("unexpected attempt row: %#v", attempts[0])
	}
	var errors []requestErrorRecord
	if err := svc.usage.db.Find(&errors).Error; err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 || errors[0].ErrorType != "upstream-timeout" || !errors[0].Retryable {
		t.Fatalf("unexpected error rows: %#v", errors)
	}
}

func TestDecisionTelemetryDisabledByDefault(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_decision_default",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	svc := newTestService(t, upstream.URL, "provider-key")
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertDecisionTelemetryCounts(t, svc, 0, 0, 0, 0, 0)
}

func TestDecisionTelemetryEnabledRecordsTextCandidateAndDecision(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_decision_enabled",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertDecisionTelemetryCounts(t, svc, 18, 1, 0, 1, 1)
	assertDecisionShapeBool(t, svc, "reasoning_requested", false)
	var selected int64
	if err := svc.usage.db.Model(&decisionTargetCandidateRecord{}).Where("selected = ?", true).Count(&selected).Error; err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Fatalf("selected candidate rows=%d, want 1", selected)
	}
	var decision routingDecisionRecord
	if err := svc.usage.db.First(&decision).Error; err != nil {
		t.Fatal(err)
	}
	if decision.Strategy != "static" || decision.Provider != "mock" || decision.Model != "mock-model" || decision.SelectedCandidateIndex != 0 {
		t.Fatalf("unexpected routing decision: %#v", decision)
	}
	var term dynamicScoreTermRecord
	if err := svc.usage.db.Where("term_name = ? AND score_name = ?", "configured_order", "configured_order").First(&term).Error; err != nil {
		t.Fatal(err)
	}
	if term.CandidateIndex != 0 || term.Rank != 1 || !term.Selected {
		t.Fatalf("unexpected static ranking term: %#v", term)
	}
}

func TestDecisionTelemetryRecordsExternalPolicyFailureBeforeDecision(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when policy fails closed")
	}))
	defer upstream.Close()
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 99})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Models["external-policy"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        policy.URL + "/route",
			AllowHosts: []string{policyURL.Hostname()},
			TimeoutMS:  500,
		},
		Targets: []Target{{Provider: "mock", Model: "cheap-model", Weight: 1}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-policy")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"external-policy","messages":[{"role":"user","content":"secret prompt"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "routing-policy-error") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var executions []policyExecutionRecord
	if err := svc.usage.db.Find(&executions).Error; err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 {
		t.Fatalf("policy executions=%d: %#v", len(executions), executions)
	}
	execution := executions[0]
	if execution.Outcome != "error" || execution.ErrorClass != "external-policy-invalid-target" || execution.SelectedCandidateIndex != -1 || execution.EligibleTargetCount != 1 {
		t.Fatalf("unexpected policy execution: %#v", execution)
	}
	for _, forbidden := range []string{"secret prompt", "provider-key", cfg.Callers[0].TokenSHA256, testToken} {
		if strings.Contains(execution.ErrorMessage, forbidden) {
			t.Fatalf("policy execution leaked %q: %#v", forbidden, execution)
		}
	}
	var decisions int64
	if err := svc.usage.db.Model(&routingDecisionRecord{}).Count(&decisions).Error; err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatalf("routing decision rows=%d, want 0", decisions)
	}
}

func TestRoutingFingerprintsRedactSecretsAndTrackRoutingChanges(t *testing.T) {
	cfg := testConfig(t, "https://upstream.example", "provider-key-a", t.TempDir())
	cfg.Provider["mock"] = ProviderConfig{BaseURL: "https://private-a.example/v1", Dialect: "openai", APIKey: "provider-key-a", APIKeyEnv: "PROVIDER_KEY_A"}
	cfg.Models["default"] = ModelGroup{Strategy: "weighted", Targets: []Target{{Provider: "mock", Model: "a", Weight: 10}}}

	secretChanged := testConfig(t, "https://upstream.example", "provider-key-b", t.TempDir())
	secretChanged.Provider["mock"] = ProviderConfig{BaseURL: "https://private-b.example/v1", Dialect: "openai", APIKey: "provider-key-b", APIKeyEnv: "PROVIDER_KEY_B"}
	secretChanged.Models["default"] = cfg.Models["default"]
	if routingConfigFingerprint(cfg) != routingConfigFingerprint(secretChanged) {
		t.Fatal("routing fingerprint changed for provider secret/base-url-only change")
	}

	routingChanged := testConfig(t, "https://upstream.example", "provider-key-a", t.TempDir())
	routingChanged.Provider["mock"] = cfg.Provider["mock"]
	routingChanged.Models["default"] = ModelGroup{Strategy: "weighted", Targets: []Target{{Provider: "mock", Model: "a", Weight: 20}}}
	if modelGroupConfigFingerprint("default", cfg.Models["default"]) == modelGroupConfigFingerprint("default", routingChanged.Models["default"]) {
		t.Fatal("model group fingerprint did not change for target weight change")
	}

	pricingChanged := testConfig(t, "https://upstream.example", "provider-key-a", t.TempDir())
	pricingChanged.Provider["mock"] = cfg.Provider["mock"]
	provider := pricingChanged.Provider["mock"]
	provider.Models = map[string]ProviderModel{"a": {Model: "a", InputPricePerMillionUSD: 2}}
	pricingChanged.Provider["mock"] = provider
	if pricingCatalogFingerprint(cfg) == pricingCatalogFingerprint(pricingChanged) {
		t.Fatal("pricing fingerprint did not change for catalog price change")
	}
}

func TestDecisionTelemetryRecordsNoEligibleFilterReason(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "tool-only", ToolOnly: true}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertDecisionTelemetryCounts(t, svc, 18, 1, 1, 0, 0)
	assertDecisionFilterReason(t, svc, "tool-only-target")
}

func TestDecisionTelemetryRecordsToolSupportFilterReason(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "plain-chat"}}}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	body := `{"model":"default","messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), `"type":"no-eligible-target"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertDecisionTelemetryCounts(t, svc, 18, 1, 1, 0, 0)
	assertDecisionFilterReason(t, svc, "tool-support")
}

func TestDecisionTelemetryRecordsCacheBypassReason(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "up_cache_bypass",
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Cache-Control", "no-cache")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var reason decisionCacheReasonRecord
	if err := svc.usage.db.First(&reason).Error; err != nil {
		t.Fatal(err)
	}
	if reason.Status != "bypass" || reason.Reason != "cache-request-no-cache" {
		t.Fatalf("unexpected cache reason: %#v", reason)
	}
}

func assertDecisionTelemetryCounts(t *testing.T, svc *Service, shape, candidates, filters, decisions, cacheReasons int64) {
	t.Helper()
	got := []struct {
		name  string
		want  int64
		model any
	}{
		{name: "shape", want: shape, model: &decisionShapeFeatureRecord{}},
		{name: "candidates", want: candidates, model: &decisionTargetCandidateRecord{}},
		{name: "filters", want: filters, model: &decisionTargetFilterReasonRecord{}},
		{name: "decisions", want: decisions, model: &routingDecisionRecord{}},
		{name: "cache reasons", want: cacheReasons, model: &decisionCacheReasonRecord{}},
	}
	for _, item := range got {
		var count int64
		if err := svc.usage.db.Model(item.model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != item.want {
			t.Fatalf("%s rows=%d, want %d", item.name, count, item.want)
		}
	}
}

func assertDecisionShapeBool(t *testing.T, svc *Service, name string, want bool) {
	t.Helper()
	var feature decisionShapeFeatureRecord
	if err := svc.usage.db.Where("feature_name = ?", name).First(&feature).Error; err != nil {
		t.Fatal(err)
	}
	if feature.BoolValue != want {
		t.Fatalf("decision shape %s=%t, want %t", name, feature.BoolValue, want)
	}
}

func assertDecisionFilterReason(t *testing.T, svc *Service, want string) {
	t.Helper()
	var count int64
	if err := svc.usage.db.Model(&decisionTargetFilterReasonRecord{}).Where("reason = ?", want).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("filter reason %q count=%d, want 1", want, count)
	}
}

func cursorMixedToolsImageConfig(t *testing.T, upstreamURL, dir string, includeCombined bool) *Config {
	t.Helper()
	cfg := testConfig(t, upstreamURL, "provider-key", dir)
	cfg.Server.DefaultModelGroup = "big-coder-fixture"
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Server.DecisionTelemetry.Enabled = true
	cfg.Provider = map[string]ProviderConfig{
		"chat":      {BaseURL: upstreamURL + "/v1", Dialect: "openai-chat", APIKey: "provider-key"},
		"responses": {BaseURL: upstreamURL + "/v1", Dialect: "openai-responses", APIKey: "provider-key"},
		"anthropic": {BaseURL: upstreamURL, Dialect: "anthropic", APIKey: "provider-key"},
	}
	targets := []Target{
		{
			Provider:        "chat",
			Model:           "chat-tools-text-only",
			Weight:          100,
			ContextTokens:   200000,
			InputModalities: []string{"text"},
			ToolSupport:     ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
		},
		{
			Provider:        "responses",
			Model:           "responses-tools-image",
			Weight:          100,
			ContextTokens:   200000,
			InputModalities: []string{"text", "image"},
			ToolSupport:     ToolSupport{OpenAIResponses: []string{"function"}},
		},
		{
			Provider:        "anthropic",
			Model:           "anthropic-tools-image",
			Weight:          100,
			ContextTokens:   200000,
			InputModalities: []string{"text", "image"},
			ToolSupport:     ToolSupport{AnthropicMessages: []string{"client_tools"}},
		},
	}
	if includeCombined {
		targets = append(targets, Target{
			Provider:        "chat",
			Model:           "chat-tools-image",
			Weight:          1,
			ContextTokens:   200000,
			InputModalities: []string{"text", "image"},
			ToolSupport:     ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
		})
	}
	cfg.Models = map[string]ModelGroup{
		"big-coder-fixture": {Strategy: "weighted", Targets: targets},
	}
	cfg.Callers[0].Allow = []string{"big-coder-fixture"}
	cfg.Callers[0].Rate.TPM = 10000000
	cfg.Callers[0].Quota.Day.Tokens = 10000000
	cfg.Callers[0].Quota.Month.Tokens = 10000000
	cfg.Callers[0].Key.LifetimeTokens = 10000000
	return cfg
}

func cursorMixedToolsImagePayload(t *testing.T, marker string) string {
	t.Helper()
	tools := make([]map[string]any, 0, 19)
	for i := 0; i < 19; i++ {
		name := fmt.Sprintf("cursor_tool_%02d", i+1)
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": "Cursor regression fixture tool " + strings.Repeat("schema ", 180),
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Repository relative path " + strings.Repeat("path ", 120),
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Patch or file content " + strings.Repeat("content ", 120),
						},
					},
				},
			},
		})
	}
	body := map[string]any{
		"model":  "big-coder-fixture",
		"stream": true,
		"messages": []map[string]any{
			{"role": "system", "content": "You are editing a repository."},
			{"role": "assistant", "content": "I will inspect the code."},
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": marker + " cursor-private-secret " + strings.Repeat("large repository context ", 14000)},
				{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,RAW_CURSOR_IMAGE", "detail": "high"}},
			}},
		},
		"tools":               tools,
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertCursorMixedTelemetryDoesNotContain(t *testing.T, svc *Service, requestID string, forbidden ...string) {
	t.Helper()
	var shape requestShapeRecord
	if err := svc.usage.db.Where("request_id = ?", requestID).First(&shape).Error; err != nil {
		t.Fatal(err)
	}
	var candidates []decisionTargetCandidateRecord
	if err := svc.usage.db.Where("request_id = ?", requestID).Find(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	var reasons []decisionTargetFilterReasonRecord
	if err := svc.usage.db.Where("request_id = ?", requestID).Find(&reasons).Error; err != nil {
		t.Fatal(err)
	}
	var features []decisionShapeFeatureRecord
	if err := svc.usage.db.Where("request_id = ?", requestID).Find(&features).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]any{shape, candidates, reasons, features})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if strings.Contains(string(raw), value) {
			t.Fatalf("telemetry leaked %q in %s", value, raw)
		}
	}
}

func TestRequestShapeTelemetryPersistsForSuccessfulAndRejectedAttempts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		wantStatus int
	}{
		{name: "success", statusCode: http.StatusOK, wantStatus: http.StatusOK},
		{name: "upstream-400", statusCode: http.StatusBadRequest, wantStatus: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Fatalf("path=%s", r.URL.Path)
				}
				if tc.statusCode >= 400 {
					writeJSON(w, tc.statusCode, map[string]any{"error": map[string]any{"message": "provider rejected shape"}})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"id": "chatcmpl_shape",
					"choices": []map[string]any{{
						"message":       map[string]any{"role": "assistant", "content": "ok"},
						"finish_reason": "stop",
					}},
					"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
				})
			}))
			defer upstream.Close()
			dir := t.TempDir()
			cfg := testConfig(t, upstream.URL, "provider-key", dir)
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
			cfg.Models["default"] = ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model", ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice", "structured_outputs"}}}}}
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			body := `{
				"model":"default",
				"stream":true,
				"messages":[{"role":"user","content":"do not persist prompt ` + tc.name + `"}],
				"tools":[{"type":"function","function":{"name":"secret_lookup","parameters":{"type":"object","properties":{"secret_field":{"type":"string","description":"do not persist schema"}}}}}],
				"tool_choice":"auto",
				"store":true,
				"metadata":{"customer":"do not persist metadata"},
				"max_tokens":64
			}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("User-Agent", "codex-test")
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}

			var usage usageRecord
			if err := svc.usage.db.First(&usage).Error; err != nil {
				t.Fatal(err)
			}
			var shape requestShapeRecord
			if err := svc.usage.db.Where("request_id = ?", usage.RequestID).First(&shape).Error; err != nil {
				t.Fatal(err)
			}
			if shape.ToolCount != 1 || shape.ToolChoiceMode != "auto" || !shape.StorePresent || !shape.MetadataPresent || shape.RequestShapeFingerprint == "" || shape.ToolSchemaFingerprint == "" {
				t.Fatalf("shape=%#v", shape)
			}
			var translated requestTranslationShapeRecord
			if err := svc.usage.db.Where("request_id = ? AND attempt_index = ?", usage.RequestID, 1).First(&translated).Error; err != nil {
				t.Fatal(err)
			}
			if translated.TranslatedToolCount != 1 || translated.TranslatedToolChoiceMode != "auto" || translated.TranslatedRequestBytesBucket == "" {
				t.Fatalf("translated=%#v", translated)
			}
			var events []requestTranslationFieldEventRecord
			if err := svc.usage.db.Where("request_id = ?", usage.RequestID).Find(&events).Error; err != nil {
				t.Fatal(err)
			}
			if len(events) == 0 {
				t.Fatal("missing translation field events")
			}
			assertPersistedShapeRowsDoNotContain(t, shape, translated, events, "do not persist", "secret_lookup", "secret_field", "provider-key", testToken)
		})
	}
}

func assertPersistedShapeRowsDoNotContain(t *testing.T, shape requestShapeRecord, translated requestTranslationShapeRecord, events []requestTranslationFieldEventRecord, forbidden ...string) {
	t.Helper()
	raw, err := json.Marshal([]any{shape, translated, events})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(raw))
	for _, value := range forbidden {
		if strings.Contains(text, strings.ToLower(value)) {
			t.Fatalf("persisted shape telemetry leaked %q: %s", value, string(raw))
		}
	}
}

func traceEventsContain(events []requestTraceEventRecord, name string) bool {
	for _, event := range events {
		if event.Event == name {
			return true
		}
	}
	return false
}

const testToken = "rtr_test_token"

func newTestService(t *testing.T, upstreamURL, providerKey string) *Service {
	t.Helper()
	dir := t.TempDir()
	cfg := testConfig(t, upstreamURL, providerKey, dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func testConfig(t *testing.T, upstreamURL, providerKey, dir string) *Config {
	t.Helper()
	sum := sha256.Sum256([]byte(testToken))
	usageDBDisabled := false
	return &Config{
		Server: ServerConfig{Identifiers: IdentifierConfig{Mode: "passthrough"},
			Listen:            ":0",
			DefaultModelGroup: "default",
			Cache:             CacheConfig{Enabled: true, MaxBytes: 1 << 20, DefaultTTL: 0},
			UsageDB:           UsageDBConfig{Enable: &usageDBDisabled, MigrationPolicy: usageDBMigrationPolicyAutoSafe},
			Logging: LoggingConfig{
				Path: filepath.Join(dir, "requests.jsonl"),
			},
		},
		StatePath: filepath.Join(dir, "state.json"),
		Provider: map[string]ProviderConfig{
			"mock": {BaseURL: upstreamURL + "/v1", Dialect: "openai", APIKey: providerKey},
		},
		Models: map[string]ModelGroup{
			"default": {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "mock-model"}}},
			"other":   {Strategy: "static", Targets: []Target{{Provider: "mock", Model: "other-model"}}},
		},
		Callers: []CallerConfig{{
			ID:          "alice",
			User:        "alice",
			Project:     "metrum-insights",
			Environment: "test",
			TokenSHA256: hex.EncodeToString(sum[:]),
			TokenID:     "rtr_alice_test",
			Allow:       []string{"default", "other"},
			Rate:        RateConfig{RPM: 100, TPM: 100000, Concurrent: 4},
			Quota:       QuotaConfig{Day: BudgetConfig{Requests: 100, Tokens: 100000}, Month: BudgetConfig{Tokens: 1000000}, SoftPct: 80},
			Key:         KeyConfig{LifetimeTokens: 1000000, SoftPct: 90, OnExhaust: "disable"},
		}},
	}
}

// freshSQLiteUsageDBConfigForTest makes a fresh temporary store's migration
// intent explicit without performing I/O. Unrelated service tests use
// auto-safe; migration and deployment-job tests construct deliberate state.
func freshSQLiteUsageDBConfigForTest(path string, enabled ...*bool) UsageDBConfig {
	cfg := UsageDBConfig{}
	cfg.Driver = "sqlite"
	cfg.Path = path
	cfg.MigrationPolicy = usageDBMigrationPolicyAutoSafe
	if len(enabled) != 0 {
		cfg.Enable = enabled[0]
	}
	return cfg
}

func testPIIFilterConfig(mode string) PIIFilterConfig {
	restore := mode == "redact_and_restore"
	return PIIFilterConfig{
		Enabled:         true,
		Mode:            mode,
		RestoreResponse: &restore,
		Rules: []PIIFilterRule{
			{
				Name:              "email",
				Expression:        `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`,
				PlaceholderPrefix: "EMAIL",
			},
			{
				Name:              "phone",
				Expression:        `\b(?:\+1[-. ]?)?\(?[2-9]\d{2}\)?[-. ]?[2-9]\d{2}[-. ]?\d{4}\b`,
				PlaceholderPrefix: "PHONE",
			},
			{
				Name:              "ssn",
				Expression:        `\b\d{3}-\d{2}-\d{4}\b`,
				PlaceholderPrefix: "US_SSN",
			},
		},
	}
}

func mustJSONMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertJSONEquivalent(t *testing.T, name string, got, want any) {
	t.Helper()
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s got value is not JSON-serializable: %v", name, err)
	}
	wantRaw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("%s want value is not JSON-serializable: %v", name, err)
	}
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("%s mismatch\ngot:  %s\nwant: %s", name, gotRaw, wantRaw)
	}
}

func TestIntelligentRoutingServesEligibleBaselineInFoundationIncrement(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
	provider := cfg.Provider["mock"]
	provider.Models = map[string]ProviderModel{"selector": {Model: "selector-model", Dialect: "openai-chat"}}
	cfg.Provider["mock"] = provider
	group := ModelGroup{
		Strategy: "intelligent",
		IntelligentRouting: IntelligentRoutingConfig{
			Mode: "shadow", DecisionModel: IntelligentDecisionModel{Provider: "mock", ModelRef: "selector"},
			TimeoutMS: 250, MaxOutputTokens: 64, MaxConcurrent: 1, MaxDecisionCostUSD: 0.01,
			ContextMode: "scalar_only", OnError: "fallback", SchemaVersion: "v1",
		},
		Targets: []Target{{Provider: "mock", Model: "first"}, {Provider: "mock", Model: "second"}},
	}
	cfg.Models["default"] = group
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	decision, err := svc.pick(nil, "default", svc.cfg.Models["default"], &IRRequest{}, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Target.Model != "first" || decision.Strategy != "intelligent" || len(decision.PolicyExecutions) != 1 || decision.PolicyExecutions[0].Outcome != "baseline_only" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestExternalRoutingPolicyCannotBypassCallerAuthorizationOrEligibility(t *testing.T) {
	policyCalls := 0
	var policyPayload map[string]any
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policyCalls++
		if err := json.NewDecoder(r.Body).Decode(&policyPayload); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"target": map[string]any{"provider": "mock", "model": "blocked-model"}})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Models["external-denied"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL: policy.URL, AllowHosts: []string{policyURL.Hostname()}, Mode: "enforce",
		},
		Targets: []Target{
			{Provider: "mock", Model: "blocked-model", ToolOnly: true},
			{Provider: "mock", Model: "eligible-model"},
		},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"external-denied","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || policyCalls != 0 {
		t.Fatalf("unauthorized external group status=%d policy_calls=%d body=%s", rr.Code, policyCalls, rr.Body.String())
	}

	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external-denied")
	authorized, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer authorized.Close()
	_, err = authorized.pick(nil, "external-denied", authorized.cfg.Models["external-denied"], &IRRequest{}, "openai-chat", nil, "")
	if err == nil {
		t.Fatal("external policy selected a request-shape-filtered target")
	}
	if policyCalls != 1 {
		t.Fatalf("policy calls=%d, want 1 authorized call", policyCalls)
	}
	targets, _ := policyPayload["targets"].([]any)
	if len(targets) != 1 || policyPayload["allTargets"] != nil {
		t.Fatalf("policy received non-eligible targets: %#v", policyPayload)
	}
}

func TestExternalRoutingPolicyCannotSelectContractFilteredTarget(t *testing.T) {
	var policyPayload map[string]any
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&policyPayload); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"target": map[string]any{"provider": "mock", "model": "unapproved-model"}})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	cfg.Models["contract-policy"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL: policy.URL, AllowHosts: []string{policyURL.Hostname()}, Mode: "enforce",
		},
		Contract: &ModelGroupContract{QualityFloor: ContractQualityFloor{RequireTags: []string{"approved"}}},
		Targets: []Target{
			{Provider: "mock", Model: "unapproved-model"},
			{Provider: "mock", Model: "approved-model", Tags: []string{"approved"}},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "contract-policy")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	_, err = svc.pick(nil, "contract-policy", svc.cfg.Models["contract-policy"], &IRRequest{}, "openai-chat", nil, "")
	if err == nil {
		t.Fatal("external policy selected a contract-filtered target")
	}
	targets, _ := policyPayload["targets"].([]any)
	if len(targets) != 1 || policyPayload["allTargets"] != nil {
		t.Fatalf("policy received contract-filtered targets: %#v", policyPayload)
	}
}

func TestExternalRoutingPolicyShadowPromotionAndRollbackModes(t *testing.T) {
	policyCalls := 0
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policyCalls++
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 1, "classLabel": "adaptive:second"})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	newService := func(mode string) *Service {
		dir := t.TempDir()
		cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", dir)
		enabled := true
		cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &enabled)
		cfg.Server.DecisionTelemetry.Enabled = true
		cfg.Models["adaptive"] = ModelGroup{
			Strategy: "external",
			ExternalPolicy: ExternalPolicyConfig{
				URL: policy.URL, AllowHosts: []string{policyURL.Hostname()}, Mode: mode,
			},
			Targets: []Target{{Provider: "mock", Model: "first"}, {Provider: "mock", Model: "second"}},
		}
		cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "adaptive")
		svc, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(svc.Close)
		return svc
	}

	shadow := newService("shadow")
	rc := &requestContext{}
	shadow.recordEligibilityTelemetry(rc, "adaptive", shadow.cfg.Models["adaptive"], &IRRequest{}, "openai-chat")
	shadowDecision, err := shadow.pick(rc, "adaptive", shadow.cfg.Models["adaptive"], &IRRequest{}, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if shadowDecision.Target.Model != "first" || shadowDecision.ShadowRecommended == nil || shadowDecision.ShadowRecommended.Model != "second" ||
		len(shadowDecision.PolicyExecutions) != 1 || shadowDecision.PolicyExecutions[0].Outcome != "shadow_recommended" {
		t.Fatalf("shadow decision=%#v", shadowDecision)
	}
	shadow.recordRoutingDecisionTelemetry(rc, shadowDecision)
	if len(rc.rec.RoutingSignals) != 1 || rc.rec.RoutingSignals[0].SignalName != "shadow_recommended_candidate" || rc.rec.RoutingSignals[0].CandidateIndex != 1 {
		t.Fatalf("shadow recommendation telemetry=%#v", rc.rec.RoutingSignals)
	}

	enforce := newService("enforce")
	enforced, err := enforce.pick(nil, "adaptive", enforce.cfg.Models["adaptive"], &IRRequest{}, "openai-chat", nil, "")
	if err != nil || enforced.Target.Model != "second" {
		t.Fatalf("enforce decision=%#v err=%v", enforced, err)
	}
	backwardCompatible := newService("")
	legacy, err := backwardCompatible.pick(nil, "adaptive", backwardCompatible.cfg.Models["adaptive"], &IRRequest{}, "openai-chat", nil, "")
	if err != nil || legacy.Target.Model != "second" {
		t.Fatalf("omitted mode decision=%#v err=%v", legacy, err)
	}

	beforeBaselineCalls := policyCalls
	baseline := newService("baseline")
	rolledBack, err := baseline.pick(nil, "adaptive", baseline.cfg.Models["adaptive"], &IRRequest{}, "openai-chat", nil, "")
	if err != nil || rolledBack.Target.Model != "first" || rolledBack.PolicyExecutions[0].Outcome != "baseline" {
		t.Fatalf("baseline decision=%#v err=%v", rolledBack, err)
	}
	if policyCalls != beforeBaselineCalls {
		t.Fatalf("baseline mode called policy: before=%d after=%d", beforeBaselineCalls, policyCalls)
	}

	if routingPolicyFingerprint(shadow.cfg, "adaptive", shadow.cfg.Models["adaptive"]) ==
		routingPolicyFingerprint(enforce.cfg, "adaptive", enforce.cfg.Models["adaptive"]) {
		t.Fatal("external policy mode did not change routing policy fingerprint")
	}
}

func TestTargetRegionPersistsForServedFailoverTarget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] == "primary-model" {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": "synthetic failure"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "region_failover",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	enabled := true
	cfg := testConfig(t, upstream.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"), &enabled)
	cfg.Models["region-failover"] = ModelGroup{
		Strategy: "failover",
		Targets: []Target{
			{Provider: "mock", Model: "primary-model", Region: "deployment-region-a"},
			{Provider: "mock", Model: "fallback-model", Region: "deployment-region-b"},
		},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "region-failover")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"region-failover","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var rows []usageRecord
	if err := svc.usage.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TargetModel != "fallback-model" || rows[0].TargetRegion != "deployment-region-b" {
		t.Fatalf("served target diagnostics=%#v", rows)
	}
}

func TestMain(m *testing.M) {
	licenseRequired = false
	os.Exit(m.Run())
}
