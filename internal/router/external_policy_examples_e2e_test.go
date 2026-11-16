// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startExamplePolicy(t *testing.T, relPath string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "..", relPath), "--host", "127.0.0.1", "--port", fmt.Sprint(port))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return baseURL
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("example policy %s did not become healthy", relPath)
	return ""
}

func TestPythonPolicyExamplesRouteThroughRouter(t *testing.T) {
	for _, tc := range []struct {
		name, example, userText, wantModel string
		includeRequest                     bool
		targets                            []Target
	}{
		{
			name: "request shape", example: "examples/typescript-request-shape/request_shape_policy.py",
			userText: "hello", wantModel: "cheap-model",
			targets: []Target{{Provider: "mock", Model: "heavy-model", Tier: "heavy", Weight: 1}, {Provider: "mock", Model: "cheap-model", Tier: "cheap", Weight: 1}},
		},
		{
			name: "PII", example: "examples/typescript-pii-policy/pii_policy.py",
			userText: "ship to 12 main street", wantModel: "sensitive-model", includeRequest: true,
			targets: []Target{{Provider: "mock", Model: "normal-model", Tier: "normal", Weight: 1}, {Provider: "mock", Model: "sensitive-model", Tier: "sensitive", Weight: 1}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policyURL := startExamplePolicy(t, tc.example)
			var gotModel string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				gotModel, _ = body["model"].(string)
				writeJSON(w, http.StatusOK, map[string]any{"id": "example-policy-e2e", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
			}))
			defer upstream.Close()
			parsed, err := url.Parse(policyURL)
			if err != nil {
				t.Fatal(err)
			}
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Models["example-policy"] = ModelGroup{Strategy: "external", ExternalPolicy: ExternalPolicyConfig{URL: policyURL + "/route", AllowHosts: []string{parsed.Hostname()}, TimeoutMS: 1000, IncludeRequest: tc.includeRequest}, Targets: tc.targets}
			cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "example-policy")
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"example-policy","messages":[{"role":"user","content":%q}]}`, tc.userText)))
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			svc.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("router status %d: %s", rr.Code, rr.Body.String())
			}
			if gotModel != tc.wantModel {
				t.Fatalf("selected model %q, want %q", gotModel, tc.wantModel)
			}
		})
	}
}
