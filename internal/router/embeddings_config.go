// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"strings"
)

// embeddingStrategies are the only routing strategies an embeddings model group
// may use. Chat-shaped selectors (semantic, dynamic_score, external, script,
// intelligent) rank on prompt features and could mix vector spaces, so they are
// rejected at load time instead of failing per request.
var embeddingStrategies = map[string]bool{
	"":         true,
	"static":   true,
	"failover": true,
	"weighted": true,
	"latency":  true,
	"cost":     true,
}

func targetOutputsEmbeddings(t Target) bool {
	for _, modality := range t.OutputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "embeddings") {
			return true
		}
	}
	return false
}

// validateEmbeddingGroup enforces the vector-space contract for a model group.
// A group is either all-embeddings or not an embeddings group at all; every
// embeddings target declares the same space and the dimensions it serves.
func validateEmbeddingGroup(name string, m ModelGroup) error {
	if len(m.Targets) == 0 {
		return nil
	}
	embeddingTargets := 0
	for _, target := range m.Targets {
		if targetOutputsEmbeddings(target) {
			embeddingTargets++
		}
	}
	if embeddingTargets == 0 {
		for _, target := range m.Targets {
			if target.EmbeddingSpace != "" || len(target.EmbeddingDimensions) > 0 || target.DefaultEmbeddingDimensions != 0 {
				return fmt.Errorf("model group %s target %s configures embedding fields but does not output embeddings", name, target.Model)
			}
		}
		return nil
	}
	if embeddingTargets != len(m.Targets) {
		return fmt.Errorf("model group %s mixes embedding and non-embedding targets", name)
	}
	strategy := strings.ToLower(strings.TrimSpace(m.Strategy))
	if !embeddingStrategies[strategy] {
		return fmt.Errorf("model group %s strategy %s is not supported for embeddings", name, m.Strategy)
	}
	space := ""
	var canonical []int
	defaults := map[int]bool{}
	for _, target := range m.Targets {
		if err := validateEmbeddingInputModalities(name, target); err != nil {
			return err
		}
		targetSpace := strings.TrimSpace(target.EmbeddingSpace)
		if targetSpace == "" {
			return fmt.Errorf("model group %s target %s must declare embedding_space", name, target.Model)
		}
		if targetSpace != strings.ToLower(targetSpace) {
			return fmt.Errorf("model group %s target %s embedding_space must be lowercase", name, target.Model)
		}
		if len(targetSpace) > 64 || !embeddingSpacePattern(targetSpace) {
			return fmt.Errorf("model group %s target %s has invalid embedding_space %q", name, target.Model, targetSpace)
		}
		if space != "" && targetSpace != space {
			return fmt.Errorf("model group %s mixes embedding spaces %q and %q", name, space, targetSpace)
		}
		space = targetSpace
		if len(target.EmbeddingDimensions) == 0 {
			return fmt.Errorf("model group %s target %s must declare embedding_dimensions", name, target.Model)
		}
		seen := map[int]bool{}
		for _, dims := range target.EmbeddingDimensions {
			if dims <= 0 || dims > maxEmbeddingDimensions {
				return fmt.Errorf("model group %s target %s embedding dimension %d must be between 1 and %d", name, target.Model, dims, maxEmbeddingDimensions)
			}
			if seen[dims] {
				return fmt.Errorf("model group %s target %s repeats embedding dimension %d", name, target.Model, dims)
			}
			seen[dims] = true
		}
		if canonical == nil {
			canonical = target.EmbeddingDimensions
		} else if !sameDimensions(canonical, seen) {
			return fmt.Errorf("model group %s target %s serves dimensions %v but other targets serve %v", name, target.Model, target.EmbeddingDimensions, canonical)
		}
		if target.DefaultEmbeddingDimensions != 0 {
			if !seen[target.DefaultEmbeddingDimensions] {
				return fmt.Errorf("model group %s target %s default_embedding_dimensions %d is not in embedding_dimensions", name, target.Model, target.DefaultEmbeddingDimensions)
			}
			defaults[target.DefaultEmbeddingDimensions] = true
		}
	}
	if len(defaults) > 1 {
		return fmt.Errorf("model group %s targets disagree on default_embedding_dimensions", name)
	}
	return nil
}

func sameDimensions(canonical []int, seen map[int]bool) bool {
	if len(canonical) != len(seen) {
		return false
	}
	for _, dims := range canonical {
		if !seen[dims] {
			return false
		}
	}
	return true
}

func validateEmbeddingInputModalities(group string, target Target) error {
	for _, modality := range target.InputModalities {
		if !strings.EqualFold(strings.TrimSpace(modality), "text") {
			return fmt.Errorf("model group %s target %s embedding input_modalities may only contain text", group, target.Model)
		}
	}
	return nil
}

// maxEmbeddingDimensions bounds configured output sizes so a request cannot ask
// for an unbounded vector.
const maxEmbeddingDimensions = 8192

func embeddingSpacePattern(space string) bool {
	if space == "" {
		return false
	}
	for i, r := range space {
		alnum := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if alnum {
			continue
		}
		if i == 0 {
			return false
		}
		if r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
