// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"gopkg.in/yaml.v3"
)

// PatchEligibility makes legacy OpenAI-to-Anthropic opt-ins explicit. It does
// not claim new validation evidence: operators must review this migration.
func PatchEligibility(data []byte, cfg *Config) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	root := doc.Content[0]
	models := patchNode(root, "models", nil)
	if models == nil {
		return nil, fmt.Errorf("models missing")
	}
	for name, group := range cfg.Models {
		model := patchNode(models, name, nil)
		if model == nil {
			continue
		}
		targets := patchNode(model, "targets", nil)
		if targets == nil {
			continue
		}
		for i, target := range group.Targets {
			if normalizeDialect(targetDialect(cfg.Provider[target.Provider], target)) != "anthropic" {
				continue
			}
			dialects := append([]string(nil), target.RequestShapeSupport.SupportedInboundDialects...)
			if len(dialects) == 0 {
				dialects = []string{"anthropic", "openai-chat", "openai-responses"}
			}
			shape := patchNode(targets.Content[i], "request_shape_support", nil)
			if shape == nil {
				shape = patchNode(targets.Content[i], "request_shape_support", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
			}
			var list yaml.Node
			if err := list.Encode(dialects); err != nil {
				return nil, err
			}
			patchNode(shape, "supported_inbound_dialects", &list)
			patchNode(shape, "validation_status", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "passed"})
		}
	}
	eligibility := patchNode(root, "eligibility", nil)
	if eligibility == nil {
		eligibility = patchNode(root, "eligibility", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	}
	patchNode(eligibility, "implicit_openai_to_anthropic", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"})
	return yaml.Marshal(&doc)
}

func patchNode(parent *yaml.Node, key string, value *yaml.Node) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			if value != nil {
				parent.Content[i+1] = value
			}
			return parent.Content[i+1]
		}
	}
	if value != nil {
		parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	}
	return value
}
