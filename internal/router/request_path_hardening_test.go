// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func TestHP1ScriptVMConcurrencyCapHonorsContextCancellation(t *testing.T) {
	program, err := goja.Compile("cap.js", `
var routerScript = {
  route: function() {
    block();
    return {targetIndex: 0};
  }
};`, false)
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	created := make(chan struct{}, 2)
	var runtimeCount atomic.Int64
	strategy := &scriptStrategy{
		path:       "cap.js",
		program:    program,
		httpConfig: ScriptHTTPConfig{Enabled: true, TimeoutMS: 500},
		slots:      make(chan struct{}, 1),
		runtimeFactory: func() *goja.Runtime {
			runtimeCount.Add(1)
			created <- struct{}{}
			vm := goja.New()
			if err := vm.Set("block", func() {
				<-release
			}); err != nil {
				panic(err)
			}
			return vm
		},
	}
	targets := []Target{{Provider: "mock", Model: "model"}}

	firstDone := make(chan error, 1)
	go func() {
		_, err := strategy.Pick(context.Background(), "scripted", &IRRequest{}, nil, targets, nil, nil, "")
		firstDone <- err
	}()
	select {
	case <-created:
	case <-time.After(time.Second):
		t.Fatal("first runtime was not created")
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := strategy.Pick(waitCtx, "scripted", &IRRequest{}, nil, targets, nil, nil, "")
		secondDone <- err
	}()
	cancel()

	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("second Pick() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting script did not honor context cancellation")
	}
	if got := runtimeCount.Load(); got != 1 {
		t.Fatalf("runtime creations = %d, want 1 while slot is occupied", got)
	}

	close(release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Pick() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first script did not finish")
	}
}

func TestHP1ScriptVMStateIsNotReused(t *testing.T) {
	program, err := goja.Compile("isolation.js", `
var calls = 0;
var routerScript = {
  route: function() {
    calls++;
    return {targetIndex: calls - 1};
  }
};`, false)
	if err != nil {
		t.Fatal(err)
	}
	strategy := &scriptStrategy{
		path:           "isolation.js",
		program:        program,
		slots:          make(chan struct{}, 1),
		runtimeFactory: goja.New,
	}
	targets := []Target{{Provider: "mock", Model: "model"}}
	for i := 0; i < 2; i++ {
		if _, err := strategy.Pick(context.Background(), "scripted", &IRRequest{}, nil, targets, nil, nil, ""); err != nil {
			t.Fatalf("Pick() call %d error = %v; mutable VM state appears reused", i+1, err)
		}
	}
}

func TestHP1ScriptMaxConcurrentValidation(t *testing.T) {
	cfg := minimalConfig(t)
	cfg.Models["default"] = ModelGroup{
		Strategy:            "script",
		Script:              "router.ts",
		ScriptMaxConcurrent: scriptMaxConcurrentLimit + 1,
		Targets:             []Target{{Provider: "mock", Model: "model"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "script_max_concurrent") {
		t.Fatalf("Validate() error = %v, want script_max_concurrent bound", err)
	}

	group := cfg.Models["default"]
	group.Strategy = "weighted"
	group.Script = ""
	group.ScriptMaxConcurrent = 1
	cfg.Models["default"] = group
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "does not use script strategy") {
		t.Fatalf("Validate() error = %v, want non-script strategy rejection", err)
	}
}

func TestHP2ExternalPolicyTimeoutFailsClosedWithStableClass(t *testing.T) {
	cfg := ExternalPolicyConfig{
		URL:        "http://localhost/route",
		AllowHosts: []string{"localhost"},
		TimeoutMS:  10,
	}
	strategy := newExternalPolicyStrategy(cfg)
	strategy.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})

	targets := []Target{{Provider: "mock", Model: "model"}}
	_, err := strategy.Pick(context.Background(), "external", &IRRequest{}, nil, targets, targets, nil, nil, "", "openai-chat")
	if err == nil {
		t.Fatal("Pick() succeeded after external policy timeout")
	}
	var policyErr routingPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("Pick() error type = %T, want routingPolicyError", err)
	}
	if got := policyErr.Execution.ErrorClass; got != "external-policy-timeout" {
		t.Fatalf("timeout class = %q, want external-policy-timeout", got)
	}
	if policyErr.Execution.Outcome != "error" || policyErr.Execution.SelectedCandidateIndex != -1 {
		t.Fatalf("timeout did not fail closed: %#v", policyErr.Execution)
	}
}

func TestHP2ExternalPolicyClientLoadedOnceAndReused(t *testing.T) {
	var policyCalls atomic.Int64
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policyCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"targetIndex": 0})
	}))
	defer policy.Close()
	policyURL, err := url.Parse(policy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, "http://example.invalid", "provider-key", t.TempDir())
	cfg.Models["external"] = ModelGroup{
		Strategy: "external",
		ExternalPolicy: ExternalPolicyConfig{
			URL:        policy.URL,
			AllowHosts: []string{policyURL.Hostname()},
			TimeoutMS:  100,
		},
		Targets: []Target{{Provider: "mock", Model: "model"}},
	}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "external")
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	strategy := svc.externalPolicies["external"]
	if strategy == nil || strategy.client == nil {
		t.Fatal("external policy client was not loaded at startup")
	}
	client := strategy.client
	for i := 0; i < 2; i++ {
		if _, err := svc.pick(nil, "external", cfg.Models["external"], &IRRequest{}, "openai-chat", svc.quota.callers["alice"], "rtr_alice_test"); err != nil {
			t.Fatalf("pick %d: %v", i+1, err)
		}
		if strategy.client != client {
			t.Fatal("external policy client changed between decisions")
		}
	}
	if got := policyCalls.Load(); got != 2 {
		t.Fatalf("policy calls = %d, want 2", got)
	}
}

func TestHP3ImageURLDNSUsesOneBoundedInjectedResolver(t *testing.T) {
	var calls atomic.Int64
	var deadlinesMu sync.Mutex
	var deadlines []time.Time
	svc := &Service{
		cfg:                &Config{},
		imageURLDNSTimeout: 15 * time.Millisecond,
		imageURLLookup: func(ctx context.Context, host string) ([]net.IP, error) {
			calls.Add(1)
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("resolver context has no deadline")
			}
			deadlinesMu.Lock()
			deadlines = append(deadlines, deadline)
			deadlinesMu.Unlock()
			if host == "first.example" {
				return []net.IP{net.ParseIP("8.8.8.8")}, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	req := &IRRequest{Messages: []IRMessage{{Parts: []IRContentPart{
		{Type: "image", ImageURL: "https://first.example/image.png"},
		{Type: "image", ImageURL: "https://second.example/image.png"},
	}}}}

	err := svc.validateImageURLsForUpstream(context.Background(), req)
	if got := imageURLValidationErrorClass(err); got != "image_url_dns_timeout" {
		t.Fatalf("DNS timeout class = %q, want image_url_dns_timeout (err=%v)", got, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("resolver calls = %d, want 2", got)
	}
	deadlinesMu.Lock()
	defer deadlinesMu.Unlock()
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("resolver deadlines = %v, want one request-wide deadline", deadlines)
	}
}

func TestHP3ImageURLDNSFailureClassIsStable(t *testing.T) {
	svc := &Service{
		cfg:                &Config{},
		imageURLDNSTimeout: time.Second,
		imageURLLookup: func(context.Context, string) ([]net.IP, error) {
			return nil, errors.New("resolver details that must not escape")
		},
	}
	req := &IRRequest{Messages: []IRMessage{{Parts: []IRContentPart{{
		Type: "image", ImageURL: "https://unresolvable.example/image.png",
	}}}}}
	err := svc.validateImageURLsForUpstream(context.Background(), req)
	if got := imageURLValidationErrorClass(err); got != "image_url_dns_failure" {
		t.Fatalf("DNS failure class = %q, want image_url_dns_failure", got)
	}
	if err == nil || strings.Contains(err.Error(), "resolver details") {
		t.Fatalf("DNS error is not safely sanitized: %v", err)
	}
}

func TestHP3ImageURLDNSFailureStopsBeforeRoutingAndUpstream(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, "provider-key", t.TempDir())
	cfg.Models["default"] = ModelGroup{
		Strategy: "static",
		Targets:  []Target{{Provider: "mock", Model: "vision", InputModalities: []string{"text", "image"}}},
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	var resolverCalls atomic.Int64
	svc.imageURLLookup = func(context.Context, string) ([]net.IP, error) {
		resolverCalls.Add(1)
		return nil, errors.New("synthetic DNS failure")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"default","messages":[{"role":"user","content":[{"type":"text","text":"read"},{"type":"image_url","image_url":{"url":"https://image.example/receipt.png"}}]}]}`,
	))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Router-Error-Class"); got != "image_url_dns_failure" {
		t.Fatalf("X-Router-Error-Class = %q, want image_url_dns_failure", got)
	}
	if got := rr.Header().Get("X-Metrum-Error-Class"); got != "image_url_dns_failure" {
		t.Fatalf("X-Metrum-Error-Class = %q, want image_url_dns_failure", got)
	}
	if got := resolverCalls.Load(); got != 1 {
		t.Fatalf("resolver calls = %d, want exactly one request-level validation", got)
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func BenchmarkHP1ScriptStrategyPickFreshVM(b *testing.B) {
	program, err := goja.Compile("benchmark.js", `
var routerScript = {
  route: function(ctx) {
    return {targetIndex: ctx.context.hasTools ? 1 : 0};
  }
};`, false)
	if err != nil {
		b.Fatal(err)
	}
	strategy := &scriptStrategy{
		path:           "benchmark.js",
		program:        program,
		slots:          make(chan struct{}, scriptDefaultMaxConcurrent),
		runtimeFactory: goja.New,
	}
	targets := []Target{
		{Provider: "mock", Model: "first"},
		{Provider: "mock", Model: "second"},
	}
	req := &IRRequest{Messages: []IRMessage{{Role: "user", Content: "route me"}}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := strategy.Pick(context.Background(), "scripted", req, nil, targets, nil, nil, ""); err != nil {
			b.Fatal(err)
		}
	}
}
