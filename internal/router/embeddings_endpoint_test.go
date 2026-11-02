// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type embeddingsUpstreamStub struct {
	calls    atomic.Int64
	lastBody atomic.Value // string
	lastPath atomic.Value // string
	server   *httptest.Server
	status   int
	response string
	headers  http.Header
}

func (s *embeddingsUpstreamStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.lastPath.Store(r.URL.Path)
		body := make([]byte, 0, 1024)
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				body = append(body, buf[:n]...)
			}
			if err != nil {
				break
			}
		}
		s.lastBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		for name, values := range s.headers {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		status := s.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(s.response))
	}))
	t.Cleanup(s.server.Close)
	return s.server
}

func embeddingsTestService(t *testing.T, stub *embeddingsUpstreamStub, mutate func(*Config)) *Service {
	t.Helper()
	server := stub.start(t)
	cfg := testConfig(t, server.URL, "provider-key", t.TempDir())
	cfg.Models["embed"] = ModelGroup{Strategy: "failover", Targets: []Target{{
		Provider:                   "mock",
		Model:                      "embed-model-1",
		Dialect:                    "openai",
		InputModalities:            []string{"text"},
		OutputModalities:           []string{"embeddings"},
		EmbeddingSpace:             "test-space-v1",
		EmbeddingDimensions:        []int{3, 8},
		DefaultEmbeddingDimensions: 3,
		InputPricePerMillionUSD:    0.02,
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "embed")
	if mutate != nil {
		mutate(cfg)
	}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func embeddingsPost(t *testing.T, svc *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rr, req)
	return rr
}

func embeddingsVectorResponse(t *testing.T, dimensions int, usagePromptTokens int) string {
	t.Helper()
	one := make([]float64, dimensions)
	return `{"object":"list","data":[{"object":"embedding","index":0,"embedding":` + embeddingsJSON(t, one) + `}],"model":"embed-model-1","usage":{"prompt_tokens":` + itoa(usagePromptTokens) + `,"total_tokens":` + itoa(usagePromptTokens) + `}}`
}

func embeddingsJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func itoa(v int) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestEmbeddingsEndpointServesAndMeters(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 7)}
	svc := embeddingsTestService(t, stub, nil)
	rr := embeddingsPost(t, svc, `{"model":"embed","input":"hello world"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := stub.lastPath.Load(); got != "/v1/embeddings" {
		t.Fatalf("upstream path=%v", got)
	}
	if got := stub.lastBody.Load(); !strings.Contains(got.(string), `"model":"embed-model-1"`) {
		t.Fatalf("upstream body=%v", got)
	}
	var body struct {
		Model string `json:"model"`
		Data  []struct {
			Index int `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "embed-model-1" {
		t.Fatalf("caller-facing model=%q", body.Model)
	}
	if rr.Header().Get("X-Metrum-Usage-Source") != "reported" {
		t.Fatalf("usage source=%q", rr.Header().Get("X-Metrum-Usage-Source"))
	}
}

func TestEmbeddingsRequestValidationFailsClosed(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 7)}
	svc := embeddingsTestService(t, stub, nil)
	for name, body := range map[string]string{
		"empty body":       ``,
		"missing input":    `{"model":"embed"}`,
		"empty string":     `{"model":"embed","input":"   "}`,
		"empty list":       `{"model":"embed","input":[]}`,
		"bad encoding":     `{"model":"embed","input":"x","encoding_format":"int8"}`,
		"bad dimensions":   `{"model":"embed","input":"x","dimensions":-4}`,
		"unknown model":    `{"model":"nope","input":"x"}`,
		"chat model group": `{"model":"default","input":"x"}`,
		"unsupported dims": `{"model":"embed","input":"x","dimensions":9}`,
		"trailing json":    `{"model":"embed","input":"x"}{}`,
		"token mismatch":   `{"model":"embed","input":["x", 3]}`,
	} {
		rr := embeddingsPost(t, svc, body)
		if rr.Code < 400 {
			t.Fatalf("%s: expected failure, got %d %s", name, rr.Code, rr.Body.String())
		}
	}
	if stub.calls.Load() != 0 {
		t.Fatalf("invalid requests reached upstream: %d", stub.calls.Load())
	}
}

func TestEmbeddingsTokenArrayRequiresTextPIIOrPassesWithoutPolicy(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 4)}
	svc := embeddingsTestService(t, stub, nil)
	rr := embeddingsPost(t, svc, `{"model":"embed","input":[11,12,13]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("token array without PII policy status=%d body=%s", rr.Code, rr.Body.String())
	}

	stub2 := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 4)}
	blocked := embeddingsTestService(t, stub2, func(cfg *Config) {
		group := cfg.Models["embed"]
		group.PIIFilter = PIIFilterConfig{Enabled: true, Mode: "redact_only", Rules: []PIIFilterRule{{Name: "email", Expression: `[a-z]+@[a-z]+\.com`, PlaceholderPrefix: "EMAIL"}}}
		cfg.Models["embed"] = group
	})
	rr = embeddingsPost(t, blocked, `{"model":"embed","input":[11,12,13]}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "pii-filter-blocked") {
		t.Fatalf("token array with PII policy status=%d body=%s", rr.Code, rr.Body.String())
	}
	if stub2.calls.Load() != 0 {
		t.Fatalf("PII-blocked request reached upstream")
	}
}

func TestEmbeddingsPIIRedactsTextInput(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 4)}
	svc := embeddingsTestService(t, stub, func(cfg *Config) {
		group := cfg.Models["embed"]
		group.PIIFilter = PIIFilterConfig{Enabled: true, Mode: "redact_only", Rules: []PIIFilterRule{{Name: "email", Expression: `[a-z]+@[a-z]+\.com`, PlaceholderPrefix: "EMAIL"}}}
		cfg.Models["embed"] = group
	})
	rr := embeddingsPost(t, svc, `{"model":"embed","input":"contact alice@example.com"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(stub.lastBody.Load().(string), "alice@example.com") {
		t.Fatal("raw PII reached upstream")
	}
}

func TestEmbeddingsResponseValidationFailsClosed(t *testing.T) {
	for name, response := range map[string]string{
		"wrong count":       `{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}],"model":"m"}`,
		"duplicate index":   `{"data":[{"index":0,"embedding":[0.1,0.2,0.3]},{"index":0,"embedding":[0.1,0.2,0.3]}],"model":"m"}`,
		"wrong dimensions":  `{"data":[{"index":0,"embedding":[0.1,0.2]}],"model":"m"}`,
		"missing embedding": `{"data":[{"index":0}],"model":"m"}`,
		"not json":          `not-json`,
	} {
		stub := &embeddingsUpstreamStub{response: response}
		svc := embeddingsTestService(t, stub, nil)
		rr := embeddingsPost(t, svc, `{"model":"embed","input":["a","b"]}`)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("%s: status=%d body=%s", name, rr.Code, rr.Body.String())
		}
	}
}

func TestEmbeddingsUpstreamErrorsAreSanitized(t *testing.T) {
	stub := &embeddingsUpstreamStub{status: http.StatusForbidden, response: `{"error":"provider secret detail"}`}
	svc := embeddingsTestService(t, stub, nil)
	rr := embeddingsPost(t, svc, `{"model":"embed","input":"x"}`)
	if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "provider secret detail") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stub2 := &embeddingsUpstreamStub{status: http.StatusInternalServerError, response: `{}`}
	svc2 := embeddingsTestService(t, stub2, nil)
	rr = embeddingsPost(t, svc2, `{"model":"embed","input":"x"}`)
	if rr.Code != http.StatusBadGateway || strings.Contains(rr.Body.String(), "upstream secret") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestEmbeddingsBypassesCache(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 4)}
	svc := embeddingsTestService(t, stub, nil)
	for i := 0; i < 2; i++ {
		if rr := embeddingsPost(t, svc, `{"model":"embed","input":"same text"}`); rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	if stub.calls.Load() != 2 {
		t.Fatalf("embeddings reused a cache: upstream calls=%d", stub.calls.Load())
	}
}

func TestEmbeddingsRejectsNonOpenAIUpstream(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 4)}
	svc := embeddingsTestService(t, stub, func(cfg *Config) {
		group := cfg.Models["embed"]
		group.Targets[0].Dialect = "anthropic"
		cfg.Models["embed"] = group
	})
	rr := embeddingsPost(t, svc, `{"model":"embed","input":"x"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if stub.calls.Load() != 0 {
		t.Fatal("non-OpenAI upstream was called")
	}
}

func TestEmbeddingsRejectsUnallowedModelGroup(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 4)}
	svc := embeddingsTestService(t, stub, func(cfg *Config) {
		cfg.Callers[0].Allow = []string{"default", "other"}
	})
	rr := embeddingsPost(t, svc, `{"model":"embed","input":"x"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if stub.calls.Load() != 0 {
		t.Fatal("disallowed group reached upstream")
	}
}

func TestEmbeddingsUsageRecord(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 7)}
	dir := t.TempDir()
	server := stub.start(t)
	cfg := testConfig(t, server.URL, "provider-key", dir)
	cfg.Server.UsageDB = freshSQLiteUsageDBConfigForTest(filepath.Join(dir, "usage.sqlite"))
	cfg.Models["embed"] = ModelGroup{Strategy: "static", Targets: []Target{{
		Provider: "mock", Model: "embed-model-1", Dialect: "openai",
		InputModalities: []string{"text"}, OutputModalities: []string{"embeddings"},
		EmbeddingSpace: "test-space-v1", EmbeddingDimensions: []int{3}, DefaultEmbeddingDimensions: 3,
		InputPricePerMillionUSD: 0.5,
	}}}
	cfg.Callers[0].Allow = append(cfg.Callers[0].Allow, "embed")
	svc, err := newServiceWithVerifiedFixture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if rr := embeddingsPost(t, svc, `{"model":"embed","input":"metered text"}`); rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	flushUsageForTest(t, svc)
	var row usageRecord
	if err := svc.usage.db.Order("ts DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Endpoint != "embeddings" || row.UsageSource != "reported" || row.InputTokens != 7 || row.OutputTokens != 0 || row.TotalTokens != 7 {
		t.Fatalf("bad embeddings usage row: endpoint=%q source=%q in=%d out=%d total=%d", row.Endpoint, row.UsageSource, row.InputTokens, row.OutputTokens, row.TotalTokens)
	}
	if row.CallerID != "alice" || row.TargetProvider != "mock" || row.TargetModel != "embed-model-1" || row.RequestedModel != "embed" {
		t.Fatalf("bad attribution: %+v", row)
	}
	if row.TotalCostUSD <= 0 {
		t.Fatalf("input price was not applied: %v", row.TotalCostUSD)
	}
}

func TestEmbeddingsModelIdentityRequestedGroup(t *testing.T) {
	stub := &embeddingsUpstreamStub{response: embeddingsVectorResponse(t, 3, 7)}
	svc := embeddingsTestService(t, stub, func(cfg *Config) {
		cfg.Server.Responses.ModelIdentity = ModelIdentityRequestedGroup
	})
	rr := embeddingsPost(t, svc, `{"model":"embed","input":"x"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "embed" {
		t.Fatalf("caller-facing model=%q", body.Model)
	}
}
