// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"strings"
)

func isEmbeddingTarget(target Target) bool {
	return len(target.OutputModalities) == 1 && strings.TrimSpace(target.OutputModalities[0]) == "embeddings"
}

func isEmbeddingGroup(group ModelGroup) bool {
	if len(group.Targets) == 0 {
		return false
	}
	for _, target := range group.Targets {
		if !isEmbeddingTarget(target) {
			return false
		}
	}
	return true
}

func validateEmbeddingGroup(name string, group *ModelGroup) error {
	count := 0
	for _, target := range group.Targets {
		hasEmbeddings := false
		for _, modality := range target.OutputModalities {
			if strings.TrimSpace(modality) == "embeddings" {
				hasEmbeddings = true
			}
		}
		if hasEmbeddings {
			if !isEmbeddingTarget(target) {
				return fmt.Errorf("model group %s mixes embeddings with other output modalities", name)
			}
			count++
		} else if target.EmbeddingSpace != "" || len(target.EmbeddingDimensions) != 0 {
			return fmt.Errorf("model group %s has embedding settings on a non-embedding target", name)
		}
	}
	if count == 0 {
		return nil
	}
	if count != len(group.Targets) {
		return fmt.Errorf("model group %s mixes embedding and non-embedding targets", name)
	}
	if strategy := strings.ToLower(strings.TrimSpace(group.Strategy)); strategy != "failover" && strategy != "weighted" {
		return fmt.Errorf("model group %s embeddings require failover or weighted strategy", name)
	}
	space := ""
	for i := range group.Targets {
		target := &group.Targets[i]
		if strings.TrimSpace(target.EmbeddingSpace) != target.EmbeddingSpace {
			return fmt.Errorf("model group %s has invalid embedding_space", name)
		}
		if target.EmbeddingSpace == "" {
			target.EmbeddingSpace = target.Provider + "/" + target.Model
		}
		if space == "" {
			space = target.EmbeddingSpace
		} else if space != target.EmbeddingSpace {
			return fmt.Errorf("model group %s embedding targets must share one embedding_space", name)
		}
		seen := map[int]bool{}
		for _, dimension := range target.EmbeddingDimensions {
			if dimension <= 0 || seen[dimension] {
				return fmt.Errorf("model group %s has invalid embedding_dimensions", name)
			}
			seen[dimension] = true
		}
	}
	return nil
}
