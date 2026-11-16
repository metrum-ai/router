// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTenantCacheIsolationEndToEnd(t *testing.T) {
	const saltEnv = "ROUTER_TEST_UPSTREAM_SALT_KEY"
	t.Setenv(saltEnv, strings.Repeat("k", 32))
	var mu sync.Mutex
	var salts []string
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		messages, _ := body["messages"].([]any)
		prompt := ""
		if len(messages) > 0 {
			if m, ok := messages[0].(map[string]any); ok {
				prompt, _ = m["content"].(string)
			}
		}
		if prompt == "FAIL_SENTINEL" {
			http.Error(w, "upstream failure", http.StatusServiceUnavailable)
			return
		}
		if prompt == "TIMEOUT_SENTINEL" {
			<-r.Context().Done()
			return
		}
		salt, _ := body["cache_salt"].(string)
		mu.Lock()
		salts = append(salts, salt)
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"chat_%d","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"fresh-%d"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`, n, n)
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Cache.UpstreamSaltKeyEnv = saltEnv
	provider := cfg.Provider["mock"]
	provider.UpstreamKind = "vllm"
	cfg.Provider["mock"] = provider
	cfg.Callers[0].Project = "tenant-a"
	other := cfg.Callers[0]
	other.ID, other.User, other.Project = "bob", "bob", "tenant-b"
	other.TokenID = "rtr_bob_test"
	otherToken := "bob-test-token"
	sum := sha256.Sum256([]byte(otherToken))
	other.TokenSHA256 = hex.EncodeToString(sum[:])
	cfg.Callers = append(cfg.Callers, other)
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	call := func(token, prompt string, ctx context.Context) *httptest.ResponseRecorder {
		body := `{"model":"default","temperature":0,"messages":[{"role":"user","content":"` + prompt + `"}]}`
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		return w
	}
	first := call(testToken, "TENANT_PROMPT_SENTINEL", context.Background())
	second := call(otherToken, "TENANT_PROMPT_SENTINEL", context.Background())
	if first.Code != http.StatusOK || second.Code != http.StatusOK || strings.Contains(first.Body.String(), "fresh-2") || !strings.Contains(second.Body.String(), "fresh-2") {
		t.Fatalf("cross-tenant response cache hit: statuses=%d/%d", first.Code, second.Code)
	}
	repeat := call(testToken, "TENANT_PROMPT_SENTINEL", context.Background())
	if repeat.Code != http.StatusOK || !strings.Contains(repeat.Body.String(), "fresh-1") {
		t.Fatalf("same-tenant cache miss: status=%d", repeat.Code)
	}
	mu.Lock()
	gotCalls := calls
	gotSalts := append([]string(nil), salts...)
	mu.Unlock()
	if gotCalls != 2 || len(gotSalts) != 2 || gotSalts[0] == "" || gotSalts[0] == gotSalts[1] {
		t.Fatalf("upstream salts/calls not tenant isolated: calls=%d salts=%d", gotCalls, len(gotSalts))
	}
	cacheItems := svc.cache.Stats().Items
	unauthorized := call("", "TENANT_PROMPT_SENTINEL", context.Background())
	if unauthorized.Code != http.StatusUnauthorized || svc.cache.Stats().Items != cacheItems {
		t.Fatal("no-caller request reached response cache")
	}
	failure := call(otherToken, "FAIL_SENTINEL", context.Background())
	if failure.Code < 500 || svc.cache.Stats().Items != cacheItems {
		t.Fatal("upstream failure created a cache entry")
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	timeout := call(otherToken, "TIMEOUT_SENTINEL", timeoutCtx)
	if timeout.Code == http.StatusOK || svc.cache.Stats().Items != cacheItems {
		t.Fatal("upstream timeout created a cache entry")
	}
	logBytes, err := os.ReadFile(cfg.Server.Logging.Path)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logBytes)
	if strings.Contains(logText, "TENANT_PROMPT_SENTINEL") || strings.Contains(logText, "FAIL_SENTINEL") || strings.Contains(logText, "TIMEOUT_SENTINEL") || strings.Contains(logText, gotSalts[0]) || strings.Contains(logText, gotSalts[1]) {
		t.Fatal("prompt or cache salt appeared in router logs")
	}
}
