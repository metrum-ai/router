// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTraceparentParsingAndPeerTrust(t *testing.T) {
	const valid = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	traceID, parentSpan, flags, ok := parseTraceparent(valid)
	if !ok || traceID != "4bf92f3577b34da6a3ce929d0e0e4736" || parentSpan != "00f067aa0ba902b7" || flags != "01" {
		t.Fatalf("parsed traceparent = (%q,%q,%q,%v)", traceID, parentSpan, flags, ok)
	}
	for _, malformed := range []string{
		"",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-zz",
		"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		valid + "," + valid,
	} {
		if _, _, _, ok := parseTraceparent(malformed); ok {
			t.Errorf("accepted malformed traceparent %q", malformed)
		}
	}
	if tracePeerTrusted("127.0.0.1:5555", []string{"192.0.2.0/24"}) {
		t.Fatal("trusted an unconfigured peer")
	}
	if !tracePeerTrusted("127.0.0.1:5555", []string{"127.0.0.1/32"}) {
		t.Fatal("did not trust configured immediate peer")
	}
}

func TestTraceConfigRejectsInvalidTrustedParentCIDR(t *testing.T) {
	for _, cidrs := range [][]string{{"not-a-cidr"}, {""}} {
		if err := validateTraceConfig(TraceConfig{TrustedParentCIDRs: cidrs}); err == nil {
			t.Errorf("accepted invalid CIDRs %#v", cidrs)
		}
	}
}

type traceHopCapture struct {
	Traceparent   string
	RequestID     string
	LocalID       string
	Authorization string
	Path          string
	Method        string
}

type traceWireState struct {
	mu           sync.Mutex
	hops         []traceHopCapture
	upstreamTP   string
	upstreamID   string
	upstreamAuth string
	upstreamPath string
}

func TestTraceCorrelationDirectAndRouterChains(t *testing.T) {
	const inboundTrace = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	for depth := 0; depth <= 3; depth++ {
		t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
			state := &traceWireState{hops: make([]traceHopCapture, depth+1)}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				state.mu.Lock()
				state.upstreamTP = r.Header.Get("traceparent")
				state.upstreamID = r.Header.Get("X-Request-Id")
				state.upstreamAuth = r.Header.Get("Authorization")
				state.upstreamPath = r.URL.Path
				state.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"chatcmpl_trace","object":"chat.completion","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer upstream.Close()

			tokens := make([]string, depth+1)
			for i := range tokens {
				tokens[i] = fmt.Sprintf("trace-hop-token-%d", i)
			}
			nextURL := upstream.URL
			servers := make([]*httptest.Server, depth+1)
			for i := depth; i >= 0; i-- {
				cfg := testConfig(t, nextURL, "provider-leaf", t.TempDir())
				provider := cfg.Provider["mock"]
				provider.BaseURL = nextURL + "/v1"
				if i < depth {
					provider.APIKey = tokens[i+1]
					group := cfg.Models["default"]
					group.Targets[0].Model = "default"
					cfg.Models["default"] = group
				}
				cfg.Provider["mock"] = provider
				sum := sha256.Sum256([]byte(tokens[i]))
				cfg.Callers[0].TokenSHA256 = hex.EncodeToString(sum[:])
				cfg.Callers[0].TrustTraceparent = i > 0
				cfg.Server.Trace.TrustedParentCIDRs = []string{"127.0.0.1/32"}
				if i == 0 {
					cfg.Server.Cache.Enabled = false
					cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
				}
				svc, err := newServiceWithVerifiedFixture(cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(svc.Close)
				index := i
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					svc.Handler().ServeHTTP(w, r)
					state.mu.Lock()
					state.hops[index] = traceHopCapture{Traceparent: r.Header.Get("traceparent"), RequestID: r.Header.Get("X-Request-Id"), LocalID: w.Header().Get("X-Request-Id"), Authorization: r.Header.Get("Authorization"), Path: r.URL.Path, Method: r.Method}
					state.mu.Unlock()
				}))
				servers[i] = server
				t.Cleanup(server.Close)
				nextURL = server.URL
			}

			request, err := http.NewRequest(http.MethodPost, servers[0].URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hello"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+tokens[0])
			request.Header.Set("traceparent", inboundTrace)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			responseBody, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				state.mu.Lock()
				defer state.mu.Unlock()
				t.Fatalf("status=%d body=%s hops=%+v upstreamPath=%q", response.StatusCode, responseBody, state.hops, state.upstreamPath)
			}
			rootID := response.Header.Get("X-Request-Id")
			if rootID == "" {
				t.Fatal("root response omitted its per-hop request ID")
			}

			state.mu.Lock()
			defer state.mu.Unlock()
			traceIDs := map[string]bool{}
			spanIDs := map[string]bool{}
			for i := 0; i <= depth; i++ {
				wire := state.hops[i]
				if i == 0 {
					if wire.Traceparent != inboundTrace {
						t.Fatalf("root did not receive the client traceparent: %q", wire.Traceparent)
					}
				} else {
					parent := state.hops[i-1]
					parentOutgoing := state.hops[i].Traceparent
					if parentOutgoing == "" || wire.RequestID != parent.LocalID {
						t.Fatalf("hop %d did not receive its parent request ID: %+v parent=%+v", i, wire, parent)
					}
				}
				outbound := state.upstreamTP
				if i < depth {
					outbound = state.hops[i+1].Traceparent
				}
				traceID, spanID, _, ok := parseTraceparent(outbound)
				if !ok || traceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
					t.Fatalf("hop %d outbound traceparent=%q", i, outbound)
				}
				if spanID == "00f067aa0ba902b7" || spanIDs[spanID] {
					t.Fatalf("hop %d reused a parent or another hop span %q", i, spanID)
				}
				spanIDs[spanID] = true
				traceIDs[traceID] = true
			}
			if len(traceIDs) != 1 || state.upstreamID != state.hops[depth].LocalID || state.upstreamAuth != "Bearer provider-leaf" {
				t.Fatalf("final upstream correlation/auth id=%q auth=%q last=%+v", state.upstreamID, state.upstreamAuth, state.hops[depth])
			}
			if depth > 0 && state.hops[1].RequestID != rootID {
				t.Fatalf("first child received request id %q, want root id %q", state.hops[1].RequestID, rootID)
			}
		})
	}
}

func TestTraceCorrelationTrustedUntrustedMalformedAndDuplicate(t *testing.T) {
	const valid = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	for _, tc := range []struct {
		name       string
		cidrs      []string
		values     []string
		wantTrace  string
		wantParent string
	}{
		{name: "trusted", cidrs: []string{"127.0.0.1/32"}, values: []string{valid}, wantTrace: "4bf92f3577b34da6a3ce929d0e0e4736", wantParent: "00f067aa0ba902b7"},
		{name: "untrusted peer", cidrs: []string{"192.0.2.0/24"}, values: []string{valid}},
		{name: "malformed", cidrs: []string{"127.0.0.1/32"}, values: []string{"not-a-traceparent"}},
		{name: "duplicate", cidrs: []string{"127.0.0.1/32"}, values: []string{valid, valid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outbound string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				outbound = r.Header.Get("traceparent")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"chatcmpl_trace","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			}))
			defer upstream.Close()
			cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
			cfg.Server.Trace.TrustedParentCIDRs = tc.cidrs
			cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
			svc, err := newServiceWithVerifiedFixture(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			server := httptest.NewServer(svc.Handler())
			defer server.Close()
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"hi"}]}`))
			request.Header.Set("Authorization", "Bearer "+testToken)
			for _, value := range tc.values {
				request.Header.Add("traceparent", value)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", response.StatusCode)
			}
			traceID, _, _, ok := parseTraceparent(outbound)
			if !ok {
				t.Fatalf("outbound traceparent=%q", outbound)
			}
			if tc.wantTrace != "" && traceID != tc.wantTrace {
				t.Fatalf("trace id=%s, want %s", traceID, tc.wantTrace)
			}
			if tc.wantTrace == "" && traceID == "4bf92f3577b34da6a3ce929d0e0e4736" {
				t.Fatalf("untrusted or malformed input trace was retained: %s", traceID)
			}
			requestID := response.Header.Get("X-Request-Id")
			row := waitTraceUsageRow(t, svc, requestID)
			if row.TraceID != traceID || row.ParentSpanID != tc.wantParent {
				t.Fatalf("usage correlation row trace=%q parent=%q want trace=%q parent=%q", row.TraceID, row.ParentSpanID, traceID, tc.wantParent)
			}
		})
	}
}

func TestTraceCorrelationDoesNotPersistHeadersOrCredentials(t *testing.T) {
	var authorization, apiKey, requestID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		apiKey = r.Header.Get("X-API-Key")
		requestID = r.Header.Get("X-Request-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_trace","choices":[{"message":{"role":"assistant","content":"safe"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	cfg := testConfig(t, upstream.URL, "provider-secret", t.TempDir())
	cfg.Server.Trace.TrustedParentCIDRs = []string{"127.0.0.1/32"}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(path)
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"private prompt"}]}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("X-API-Key", "caller-api-key")
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if authorization != "Bearer provider-secret" || apiKey != "" || requestID == "" {
		t.Fatalf("outbound auth/key/request id = %q/%q/%q", authorization, apiKey, requestID)
	}
	row := waitTraceUsageRow(t, svc, response.Header.Get("X-Request-Id"))
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private prompt") || strings.Contains(string(data), testToken) || strings.Contains(string(data), "caller-api-key") {
		t.Fatalf("usage row persisted request content or credential: %s", data)
	}
}

func TestTraceCorrelationFailureKeepsRequestTrace(t *testing.T) {
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"synthetic upstream failure"}}`)
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Trace.TrustedParentCIDRs = []string{"127.0.0.1/32"}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"failure path"}]}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("traceparent", parent)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	row := waitTraceUsageRow(t, svc, response.Header.Get("X-Request-Id"))
	if row.Status < 500 || row.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || row.ParentSpanID != "00f067aa0ba902b7" {
		t.Fatalf("failure usage row status=%d trace=%q parent=%q", row.Status, row.TraceID, row.ParentSpanID)
	}
}

func TestTraceCorrelationTimeoutKeepsRequestTrace(t *testing.T) {
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	seen := make(chan [2]string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- [2]string{r.Header.Get("traceparent"), r.Header.Get("X-Request-Id")}
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Server.Trace.TrustedParentCIDRs = []string{"127.0.0.1/32"}
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(t.TempDir(), "usage.sqlite"))
	group := cfg.Models["default"]
	group.AttemptTimeoutMS = 50
	cfg.Models["default"] = group
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	server := httptest.NewServer(svc.Handler())
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"default","messages":[{"role":"user","content":"timeout path"}]}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("traceparent", parent)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode < 500 {
		t.Fatalf("timeout response status=%d", response.StatusCode)
	}
	select {
	case observed := <-seen:
		traceID, _, _, ok := parseTraceparent(observed[0])
		if !ok || traceID != "4bf92f3577b34da6a3ce929d0e0e4736" || observed[1] != response.Header.Get("X-Request-Id") {
			t.Fatalf("timed-out upstream headers=%q/%q", observed[0], observed[1])
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out upstream was not reached")
	}
	row := waitTraceUsageRow(t, svc, response.Header.Get("X-Request-Id"))
	if row.Status < 500 || row.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || row.ParentSpanID != "00f067aa0ba902b7" {
		t.Fatalf("timeout usage row status=%d trace=%q parent=%q", row.Status, row.TraceID, row.ParentSpanID)
	}
}

func waitTraceUsageRow(t *testing.T, svc *Service, requestID string) usageRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var row usageRecord
		if err := svc.usage.db.Where("request_id = ?", requestID).First(&row).Error; err == nil {
			return row
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("usage row for request %q was not written", requestID)
	return usageRecord{}
}
