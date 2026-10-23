// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var errConflictingTrainingTools = errors.New("conflicting function declarations")

// trainingTools preserves first-declaration order and compares entire canonical
// JSON declarations, not just schemas. A conflicting name makes the row ambiguous.
func trainingTools(value any) ([]any, int, error) {
	if value == nil {
		return []any{}, 0, nil
	}
	tools, ok := value.([]any)
	if !ok {
		return nil, 0, fmt.Errorf("invalid tools array")
	}
	out := []any{}
	seen := map[string][]byte{}
	removed := 0
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			return nil, removed, fmt.Errorf("invalid tool declaration")
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok || tool["type"] != "function" {
			return nil, removed, fmt.Errorf("invalid function declaration")
		}
		name, ok := fn["name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, removed, fmt.Errorf("invalid function name")
		}
		canonical, err := json.Marshal(tool)
		if err != nil {
			return nil, removed, err
		}
		if previous, exists := seen[name]; exists {
			if !bytes.Equal(previous, canonical) {
				return nil, removed, errConflictingTrainingTools
			}
			removed++
			continue
		}
		seen[name] = canonical
		out = append(out, tool)
	}
	return out, removed, nil
}
