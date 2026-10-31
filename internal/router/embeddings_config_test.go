// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestEmbeddingGroupConfiguration(t *testing.T) {
	base := Target{Provider: "a", Model: "model", OutputModalities: []string{"embeddings"}}
	for _, tc := range []struct {
		name  string
		group ModelGroup
		valid bool
	}{
		{"single default space", ModelGroup{Strategy: "failover", Targets: []Target{base}}, true},
		{"whitespace modality", ModelGroup{Strategy: "failover", Targets: []Target{{Provider: "a", Model: "model", OutputModalities: []string{" embeddings "}}}}, true},
		{"whitespace mixed modalities", ModelGroup{Strategy: "failover", Targets: []Target{{Provider: "a", Model: "model", OutputModalities: []string{"text", " embeddings "}}}}, false},
		{"weighted shared space", ModelGroup{Strategy: "weighted", Targets: []Target{{Provider: "a", Model: "x", OutputModalities: []string{"embeddings"}, EmbeddingSpace: "shared", EmbeddingDimensions: []int{512, 1536}}, {Provider: "b", Model: "y", OutputModalities: []string{"embeddings"}, EmbeddingSpace: "shared"}}}, true},
		{"mixed target types", ModelGroup{Strategy: "failover", Targets: []Target{base, {Provider: "b", Model: "chat", OutputModalities: []string{"text"}}}}, false},
		{"mixed modalities", ModelGroup{Strategy: "failover", Targets: []Target{{Provider: "a", Model: "x", OutputModalities: []string{"text", "embeddings"}}}}, false},
		{"different spaces", ModelGroup{Strategy: "failover", Targets: []Target{base, {Provider: "b", Model: "model", OutputModalities: []string{"embeddings"}}}}, false},
		{"unsafe strategy", ModelGroup{Strategy: "dynamic_score", Targets: []Target{base}}, false},
		{"bad dimensions", ModelGroup{Strategy: "failover", Targets: []Target{{Provider: "a", Model: "x", OutputModalities: []string{"embeddings"}, EmbeddingDimensions: []int{1, 1}}}}, false},
		{"nonembedding settings", ModelGroup{Strategy: "static", Targets: []Target{{Provider: "a", Model: "x", OutputModalities: []string{"text"}, EmbeddingSpace: "bad"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEmbeddingGroup("group", &tc.group)
			if (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
			if tc.name == "single default space" && tc.group.Targets[0].EmbeddingSpace != "a/model" {
				t.Fatal(tc.group.Targets[0].EmbeddingSpace)
			}
		})
	}
}

func TestEmbeddingAdminTargetWriteFailsClosed(t *testing.T) {
	for _, viaCatalog := range []bool{false, true} {
		cfg := minimalProjectionConfig()
		g := cfg.Models["default"]
		g.Strategy = "failover"
		if viaCatalog {
			provider := cfg.Provider["mock"]
			model := provider.Models["small"]
			model.OutputModalities = []string{"embeddings"}
			provider.Models["small"] = model
			cfg.Provider["mock"] = provider
		} else {
			g.Targets[0].OutputModalities = []string{"embeddings"}
			g.Targets[0].EmbeddingSpace = "shared-space"
			g.Targets[0].EmbeddingDimensions = []int{256}
		}
		cfg.Models["default"] = g
		raw, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, _, _, _, err = prepareGroupWrite(raw, "default", GroupTargetsWriteRequest{Targets: []GroupTargetSpec{{Provider: "mock", ModelRef: "small"}}}, GroupWritePolicy{}, time.Now())
		var adminErr *ConfigAdminError
		if !errors.As(err, &adminErr) || adminErr.Type != "embedding-target-write-unsupported" {
			t.Fatalf("catalog=%v: %v", viaCatalog, err)
		}
	}
}

func TestEmbeddingGroupRejectsChatAndResponses(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	g := cfg.Models["default"]
	g.Strategy = "failover"
	g.Targets[0].OutputModalities = []string{" embeddings "}
	cfg.Models["default"] = g
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/responses/input_tokens", "/v1/responses/compact"} {
		body := `{"model":"default","input":"hi"}`
		if path == "/v1/chat/completions" {
			body = `{"model":"default","messages":[{"role":"user","content":"hi"}]}`
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "unsupported_endpoint_for_model") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}
