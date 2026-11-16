// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCompatibilityToolReliabilitySeedIsEmptyAndNeutral(t *testing.T) {
	manifest, err := loadCompatibilityToolReliabilitySeed()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Source != compatibilityToolReliabilitySeedSource || manifest.MatrixSchemaVersion != compatibilityToolReliabilityMatrixSchema || manifest.MatrixEvidence != "tests/tool_compat/matrix.json" || len(manifest.Targets) != 0 {
		t.Fatalf("unexpected compatibility seed manifest: %+v", manifest)
	}

	svc := toolReliabilityService(t)
	group := svc.cfg.Models["grp"]
	ordered := svc.orderToolAwareTargets("grp", group.ToolRouting, group.Targets, true)
	if ordered[0].Model != "cheap" {
		t.Fatalf("empty compatibility seed must keep configured weights, got %+v", ordered)
	}
}

func TestCompatibilityToolReliabilitySeedRejectsUnattributedTarget(t *testing.T) {
	data := []byte(`{"schema_version":1,"matrix_schema_version":1,"source":"compat-suite","matrix_evidence":"tests/tool_compat/matrix.json","targets":[{}]}`)
	if _, err := parseCompatibilityToolReliabilitySeed(data); err == nil {
		t.Fatal("unattributed target was accepted")
	}
}

func TestCompatibilityToolReliabilitySeedRejectsInvalidManifestEvidenceAndCounts(t *testing.T) {
	base := toolReliabilitySeedManifest{
		SchemaVersion: 1, MatrixSchemaVersion: compatibilityToolReliabilityMatrixSchema,
		Source: compatibilityToolReliabilitySeedSource, MatrixEvidence: "tests/tool_compat/matrix.json",
		Targets: []toolReliabilitySeedTarget{{Group: "grp", Provider: "mock", Model: "model", Valid: 1, Evidence: "tests/tool_compat/matrix.json#pi-openai-chat"}},
	}
	maxInt := int(^uint(0) >> 1)
	cases := []struct {
		name   string
		mutate func(*toolReliabilitySeedManifest)
	}{
		{"wrong source", func(m *toolReliabilitySeedManifest) { m.Source = "invented" }},
		{"missing evidence link", func(m *toolReliabilitySeedManifest) { m.Targets[0].Evidence = "" }},
		{"non-matrix evidence link", func(m *toolReliabilitySeedManifest) { m.Targets[0].Evidence = "tests/other.json#pi-openai-chat" }},
		{"zero counts", func(m *toolReliabilitySeedManifest) { m.Targets[0].Valid, m.Targets[0].Invalid = 0, 0 }},
		{"duplicate target", func(m *toolReliabilitySeedManifest) { m.Targets = append(m.Targets, m.Targets[0]) }},
		{"overflow counts", func(m *toolReliabilitySeedManifest) { m.Targets[0].Valid, m.Targets[0].Invalid = maxInt, 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := base
			manifest.Targets = append([]toolReliabilitySeedTarget(nil), base.Targets...)
			tc.mutate(&manifest)
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseCompatibilityToolReliabilitySeed(data); err == nil {
				t.Fatal("invalid seed manifest was accepted")
			}
		})
	}
}

func TestToolReliabilitySeedIsReplacedByLiveAtMinSamples(t *testing.T) {
	key := toolReliabilityKey("grp", "mock", "model")
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	store := newToolReliabilityStore(toolReliabilitySeedTarget{Group: "grp", Provider: "mock", Model: "model", Valid: 8, Invalid: 2})
	store.observe(key, toolCallValidation{Calls: 1, Invalid: 1}, time.Hour, now)
	seed := store.routingScore(key, 3, time.Hour, now)
	if seed.Source != compatibilityToolReliabilitySeedSource || seed.Calls != 10 || seed.Valid != 8 || seed.Invalid != 2 {
		t.Fatalf("below min_samples should use only seed: %+v", seed)
	}
	store.observe(key, toolCallValidation{Calls: 2, Valid: 2}, time.Hour, now)
	live := store.routingScore(key, 3, time.Hour, now)
	if live.Source != "live" || live.Calls != 3 || live.Valid != 2 || live.Invalid != 1 {
		t.Fatalf("live counts must replace, not sum with, seed: %+v", live)
	}
}

func TestWilsonLowerBoundRanksEvidence(t *testing.T) {
	if got := wilsonLowerBound(0, 0); got != 0 {
		t.Fatalf("empty sample=%v, want 0", got)
	}
	perfect := wilsonLowerBound(2, 2)
	large := wilsonLowerBound(180, 200)
	if !(perfect < large) {
		t.Fatalf("2/2 (%v) must not outrank 180/200 (%v)", perfect, large)
	}
	if large >= 1 || perfect <= 0 {
		t.Fatalf("lower bounds must be inside (0,1): perfect=%v large=%v", perfect, large)
	}
	if wilsonLowerBound(0, 10) != 0 {
		t.Fatalf("zero successes must score 0")
	}
	if wilsonLowerBound(10, 10) <= wilsonLowerBound(5, 10) {
		t.Fatal("more evidence of success must not score lower")
	}
}

func TestToolReliabilityStoreWindowExpiry(t *testing.T) {
	store := newToolReliabilityStore()
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	key := toolReliabilityKey("grp", "prov", "model")
	store.observe(key, toolCallValidation{Calls: 4, Valid: 3, Invalid: 1, FirstFailure: toolFailureInvalidJSON, failures: map[string]int{toolFailureInvalidJSON: 1}}, time.Minute, now)
	score := store.score(key, time.Minute, now.Add(30*time.Second))
	if score.Calls != 4 || score.Valid != 3 || score.Invalid != 1 || score.Score <= 0 {
		t.Fatalf("unexpected score: %+v", score)
	}
	expired := store.score(key, time.Minute, now.Add(2*time.Minute))
	if expired.Calls != 0 || expired.Score != 0 {
		t.Fatalf("expired window must be empty: %+v", expired)
	}
	if snapshot := store.snapshot(); len(snapshot) != 0 {
		t.Fatalf("expired record must be dropped: %+v", snapshot)
	}
}

func TestToolReliabilityStoreCountsClassesAndRestartsWindow(t *testing.T) {
	store := newToolReliabilityStore()
	start := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	key := toolReliabilityKey("grp", "prov", "model")
	store.observe(key, toolCallValidation{Calls: 1, Invalid: 1, failures: map[string]int{toolFailureSchemaViolation: 1}}, time.Minute, start)
	store.observe(key, toolCallValidation{Calls: 2, Valid: 2}, time.Minute, start.Add(2*time.Minute))
	snapshot := store.snapshot()[key]
	if snapshot.Calls != 2 || snapshot.Valid != 2 || snapshot.Invalid != 0 || len(snapshot.Classes) != 0 {
		t.Fatalf("window did not restart: %+v", snapshot)
	}
}

func toolReliabilityService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		cfg: &Config{
			Provider: map[string]ProviderConfig{"mock": {Dialect: "openai"}},
			Models: map[string]ModelGroup{
				"grp": {
					Strategy:    "tool_aware",
					ToolRouting: ToolRoutingConfig{Mode: "observe", MinSamples: 5},
					Targets: []Target{
						{Provider: "mock", Model: "cheap", Weight: 100, ContextTokens: 200000, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
						{Provider: "mock", Model: "reliable", Weight: 1, ContextTokens: 200000, ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}}},
					},
				},
			},
		},
		toolReliability: newToolReliabilityStore(),
	}
}

func (s *Service) seedReliability(t *testing.T, groupName, provider, model string, valid, invalid int) {
	t.Helper()
	cfg := s.cfg.Models[groupName].ToolRouting
	s.toolReliability.observe(
		toolReliabilityKey(groupName, provider, model),
		toolCallValidation{Calls: valid + invalid, Valid: valid, Invalid: invalid, failures: map[string]int{}},
		toolReliabilityWindow(cfg),
		time.Now().UTC(),
	)
}

func TestOrderToolAwareTargetsPrefersProvenReliability(t *testing.T) {
	svc := toolReliabilityService(t)
	group := svc.cfg.Models["grp"]
	svc.seedReliability(t, "grp", "mock", "cheap", 4, 6)
	svc.seedReliability(t, "grp", "mock", "reliable", 20, 0)
	ordered := svc.orderToolAwareTargets("grp", group.ToolRouting, group.Targets, true)
	if ordered[0].Model != "reliable" {
		t.Fatalf("expected reliable target first, got %+v", ordered)
	}
	// A tool-free request keeps the configured weight order.
	ordered = svc.orderToolAwareTargets("grp", group.ToolRouting, group.Targets, false)
	if ordered[0].Model != "cheap" {
		t.Fatalf("tool-free request must keep weight order, got %+v", ordered)
	}
}

func TestOrderToolAwareTargetsColdStartKeepsWeights(t *testing.T) {
	svc := toolReliabilityService(t)
	group := svc.cfg.Models["grp"]
	svc.seedReliability(t, "grp", "mock", "cheap", 1, 0)
	svc.seedReliability(t, "grp", "mock", "reliable", 2, 0)
	ordered := svc.orderToolAwareTargets("grp", group.ToolRouting, group.Targets, true)
	if ordered[0].Model != "cheap" {
		t.Fatalf("below min_samples must keep weight order, got %+v", ordered)
	}
}

func TestOrderToolAwareTargetsSingleReadyTargetKeepsWeights(t *testing.T) {
	svc := toolReliabilityService(t)
	group := svc.cfg.Models["grp"]
	svc.seedReliability(t, "grp", "mock", "reliable", 20, 0)
	ordered := svc.orderToolAwareTargets("grp", group.ToolRouting, group.Targets, true)
	if ordered[0].Model != "cheap" {
		t.Fatalf("a single scored target must not reorder, got %+v", ordered)
	}
}

func TestToolAwarePickUsesReliabilityOrder(t *testing.T) {
	svc := toolReliabilityService(t)
	group := svc.cfg.Models["grp"]
	svc.seedReliability(t, "grp", "mock", "cheap", 4, 6)
	svc.seedReliability(t, "grp", "mock", "reliable", 20, 0)
	req := &IRRequest{Model: "grp", Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file"}}}}
	decision, err := svc.pick(nil, "grp", group, req, "openai-chat", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Target.Model != "reliable" {
		t.Fatalf("selected %q, want the reliable target", decision.Target.Model)
	}
	if len(decision.Fallbacks) != 1 || decision.Fallbacks[0].Model != "cheap" {
		t.Fatalf("fallbacks: %+v", decision.Fallbacks)
	}
}

func TestObserveModeFeedsReliabilityStore(t *testing.T) {
	upstream := toolRoutingUpstream(t, `{"path":"notes.txt"}`)
	defer upstream.Close()
	dir := t.TempDir()
	svc, err := New(toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{Mode: "observe", MinSamples: 5}))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if serveToolRouting(t, svc, "agent-tools").Code != 200 {
		t.Fatal("request failed")
	}
	snapshot := svc.toolReliability.snapshot()
	record, ok := snapshot[toolReliabilityKey("agent-tools", "mock", "tool-model")]
	if !ok {
		t.Fatalf("no reliability record: %+v", snapshot)
	}
	if record.Calls != 1 || record.Valid != 1 || record.Invalid != 0 {
		t.Fatalf("unexpected counters: %+v", record)
	}
}

func TestToolReliabilitySelectionSourceIsPersisted(t *testing.T) {
	for _, source := range []string{compatibilityToolReliabilitySeedSource, "live"} {
		t.Run(source, func(t *testing.T) {
			upstream := toolRoutingUpstream(t, `{"path":"notes.txt"}`)
			defer upstream.Close()
			dir := t.TempDir()
			cfg := toolRoutingConfig(t, upstream.URL, dir, ToolRoutingConfig{Mode: "observe", MinSamples: 3})
			group := cfg.Models["agent-tools"]
			group.Targets = append(group.Targets, Target{
				Provider: "mock", Model: "tool-model-b", Weight: 5,
				ToolSupport: ToolSupport{OpenAIChat: []string{"tools", "tool_choice"}},
			})
			cfg.Models["agent-tools"] = group
			svc, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			keyA := toolReliabilityKey("agent-tools", "mock", "tool-model")
			keyB := toolReliabilityKey("agent-tools", "mock", "tool-model-b")
			if source == compatibilityToolReliabilitySeedSource {
				svc.toolReliability = newToolReliabilityStore(
					toolReliabilitySeedTarget{Group: "agent-tools", Provider: "mock", Model: "tool-model", Valid: 0, Invalid: 3},
					toolReliabilitySeedTarget{Group: "agent-tools", Provider: "mock", Model: "tool-model-b", Valid: 3, Invalid: 0},
				)
			} else {
				now := time.Now().UTC()
				svc.toolReliability.observe(keyA, toolCallValidation{Calls: 3, Invalid: 3}, toolReliabilityWindow(group.ToolRouting), now)
				svc.toolReliability.observe(keyB, toolCallValidation{Calls: 3, Valid: 3}, toolReliabilityWindow(group.ToolRouting), now)
			}
			response := serveToolRouting(t, svc, "agent-tools")
			if response.Code != 200 {
				svc.Close()
				t.Fatalf("request status=%d body=%s", response.Code, response.Body.String())
			}
			flushUsageForTest(t, svc)
			var persisted usageRecord
			if err := svc.usage.db.Where("request_id = ?", response.Header().Get("X-Request-Id")).First(&persisted).Error; err != nil {
				svc.Close()
				t.Fatal(err)
			}
			if persisted.TargetModel != "tool-model-b" || persisted.ToolReliabilitySource != source {
				svc.Close()
				t.Fatalf("usage row did not record selected evidence source %q: %+v", source, persisted)
			}
			svc.Close()
		})
	}
}
