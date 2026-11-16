// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const compatibilityToolReliabilitySeedJSON = `{"schema_version":1,"matrix_schema_version":1,"source":"compat-suite","matrix_evidence":"tests/tool_compat/matrix.json","targets":[]}`

const (
	compatibilityToolReliabilitySeedSource          = "compat-suite"
	compatibilityToolReliabilityMatrixSchema        = 1
	maxCompatibilityToolReliabilityTargets          = 256
	maxCompatibilityToolReliabilitySamplesPerTarget = 1000000
)

type toolReliabilitySeedManifest struct {
	SchemaVersion       int                         `json:"schema_version"`
	MatrixSchemaVersion int                         `json:"matrix_schema_version"`
	Source              string                      `json:"source"`
	MatrixEvidence      string                      `json:"matrix_evidence"`
	Targets             []toolReliabilitySeedTarget `json:"targets"`
}

type toolReliabilitySeedTarget struct {
	Group    string `json:"group"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Valid    int    `json:"valid"`
	Invalid  int    `json:"invalid"`
	Evidence string `json:"evidence"`
}

// loadCompatibilityToolReliabilitySeed validates the checked-in seed manifest.
// Matrix schema v1 has no target-attributed tool-failure outcomes, so its
// manifest is intentionally empty and routing starts with configured weights.
func loadCompatibilityToolReliabilitySeed() (toolReliabilitySeedManifest, error) {
	return parseCompatibilityToolReliabilitySeed([]byte(compatibilityToolReliabilitySeedJSON))
}

func parseCompatibilityToolReliabilitySeed(data []byte) (toolReliabilitySeedManifest, error) {
	var manifest toolReliabilitySeedManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return toolReliabilitySeedManifest{}, fmt.Errorf("decode compatibility tool reliability seed: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability seed has trailing JSON")
		}
		return toolReliabilitySeedManifest{}, fmt.Errorf("decode trailing compatibility tool reliability seed: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability seed schema_version must be 1")
	}
	if manifest.MatrixSchemaVersion != compatibilityToolReliabilityMatrixSchema {
		return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability matrix_schema_version must be %d", compatibilityToolReliabilityMatrixSchema)
	}
	if manifest.Source != compatibilityToolReliabilitySeedSource {
		return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability source must be %q", compatibilityToolReliabilitySeedSource)
	}
	if manifest.MatrixEvidence != "tests/tool_compat/matrix.json" {
		return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability matrix_evidence must name tests/tool_compat/matrix.json")
	}
	if len(manifest.Targets) > maxCompatibilityToolReliabilityTargets {
		return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability seed exceeds %d targets", maxCompatibilityToolReliabilityTargets)
	}
	seen := make(map[string]struct{}, len(manifest.Targets))
	for i, target := range manifest.Targets {
		for name, value := range map[string]string{"group": target.Group, "provider": target.Provider, "model": target.Model} {
			if value == "" || strings.ContainsAny(value, "|\x00\r\n") {
				return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability target %d has invalid %s", i, name)
			}
		}
		if target.Valid < 0 || target.Invalid < 0 ||
			target.Valid > maxCompatibilityToolReliabilitySamplesPerTarget ||
			target.Invalid > maxCompatibilityToolReliabilitySamplesPerTarget-target.Valid ||
			(target.Valid == 0 && target.Invalid == 0) {
			return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability target %d has invalid outcome counts", i)
		}
		if !strings.HasPrefix(target.Evidence, "tests/tool_compat/matrix.json#") || len(target.Evidence) > 256 {
			return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability target %d must link to a matrix cell", i)
		}
		key := toolReliabilityKey(target.Group, target.Provider, target.Model)
		if _, ok := seen[key]; ok {
			return toolReliabilitySeedManifest{}, fmt.Errorf("compatibility tool reliability seed has duplicate target %d", i)
		}
		seen[key] = struct{}{}
	}
	return manifest, nil
}
