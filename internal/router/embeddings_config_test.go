// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

func embeddingTestGroup(targets ...Target) ModelGroup {
	return ModelGroup{Strategy: "weighted", Targets: targets}
}

func embeddingTestTarget(model string) Target {
	return Target{
		Provider:            "mock",
		Model:               model,
		Dialect:             "openai",
		OutputModalities:    []string{"embeddings"},
		InputModalities:     []string{"text"},
		EmbeddingSpace:      "openai-text-embedding-3-small-v1",
		EmbeddingDimensions: []int{256, 1536},
	}
}

func TestValidateEmbeddingGroupAcceptsConsistentSpaces(t *testing.T) {
	group := embeddingTestGroup(embeddingTestTarget("a"), embeddingTestTarget("b"))
	if err := validateEmbeddingGroup("embed", group); err != nil {
		t.Fatal(err)
	}
	withDefault := embeddingTestGroup(embeddingTestTarget("a"))
	withDefault.Targets[0].DefaultEmbeddingDimensions = 1536
	if err := validateEmbeddingGroup("embed", withDefault); err != nil {
		t.Fatal(err)
	}
	if err := validateEmbeddingGroup("chat", ModelGroup{Strategy: "static", Targets: []Target{{Provider: "mock", Model: "chat"}}}); err != nil {
		t.Fatalf("non-embedding group rejected: %v", err)
	}
}

func TestValidateEmbeddingGroupFailsClosed(t *testing.T) {
	cases := map[string]func() ModelGroup{
		"mixed group": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets = append(group.Targets, Target{Provider: "mock", Model: "chat"})
			return group
		},
		"missing space": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].EmbeddingSpace = ""
			return group
		},
		"uppercase space": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].EmbeddingSpace = "OpenAI-V1"
			return group
		},
		"bad space": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].EmbeddingSpace = "openai v1"
			return group
		},
		"space mismatch": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"), embeddingTestTarget("b"))
			group.Targets[1].EmbeddingSpace = "other-space-v1"
			return group
		},
		"missing dimensions": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].EmbeddingDimensions = nil
			return group
		},
		"zero dimension": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].EmbeddingDimensions = []int{0}
			return group
		},
		"oversized dimension": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].EmbeddingDimensions = []int{maxEmbeddingDimensions + 1}
			return group
		},
		"duplicate dimension": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].EmbeddingDimensions = []int{256, 256}
			return group
		},
		"default outside list": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].DefaultEmbeddingDimensions = 3072
			return group
		},
		"conflicting defaults": func() ModelGroup {
			second := embeddingTestTarget("b")
			second.DefaultEmbeddingDimensions = 256
			group := embeddingTestGroup(embeddingTestTarget("a"), second)
			group.Targets[0].DefaultEmbeddingDimensions = 1536
			return group
		},
		"uneven dimensions": func() ModelGroup {
			second := embeddingTestTarget("b")
			second.EmbeddingDimensions = []int{256, 1024}
			return embeddingTestGroup(embeddingTestTarget("a"), second)
		},
		"non-text input": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Targets[0].InputModalities = []string{"image"}
			return group
		},
		"semantic strategy": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Strategy = "semantic"
			return group
		},
		"dynamic_score strategy": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Strategy = "dynamic_score"
			return group
		},
		"external strategy": func() ModelGroup {
			group := embeddingTestGroup(embeddingTestTarget("a"))
			group.Strategy = "external"
			return group
		},
		"embedding fields without embeddings": func() ModelGroup {
			return ModelGroup{Strategy: "static", Targets: []Target{{
				Provider: "mock", Model: "chat", EmbeddingSpace: "space-v1", EmbeddingDimensions: []int{256},
			}}}
		},
	}
	for name, build := range cases {
		if err := validateEmbeddingGroup("embed", build()); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestEmbeddingConfigLoads(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:1", "provider-key", t.TempDir())
	target := embeddingTestTarget("embed-model")
	target.DefaultEmbeddingDimensions = 1536
	cfg.Models["embed"] = ModelGroup{Strategy: "failover", Targets: []Target{target}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	group := cfg.Models["embed"]
	got := group.Targets[0]
	if got.EmbeddingSpace != "openai-text-embedding-3-small-v1" || got.DefaultEmbeddingDimensions != 1536 || len(got.EmbeddingDimensions) != 2 {
		t.Fatalf("embedding target not preserved: %+v", got)
	}
}
