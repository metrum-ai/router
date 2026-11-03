// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// piiDocsFiles are the published surfaces that document pii_filter.
var piiDocsFiles = []string{
	"../../docs-site/docs/configuration/pii-filtering.md",
	"../../docs/PII_FILTERING.md",
	"../../README.md",
}

var yamlFenceRE = regexp.MustCompile("(?s)```yaml\n(.*?)\n```")

// TestPIIDocsExamplesValidate keeps every published pii_filter example
// configurable: an option that startup validation rejects must not be
// presented as a supported serving option (issue #675).
func TestPIIDocsExamplesValidate(t *testing.T) {
	checked := 0
	for _, file := range piiDocsFiles {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range yamlFenceRE.FindAllStringSubmatch(string(raw), -1) {
			var doc any
			if err := yaml.Unmarshal([]byte(match[1]), &doc); err != nil {
				// Fences that are not standalone YAML documents (snippets with
				// placeholders) are out of scope for this check.
				continue
			}
			for group, cfg := range piiFiltersInDoc(t, doc) {
				checked++
				if err := validatePIIFilter(group, cfg); err != nil {
					t.Fatalf("%s: pii_filter example for group %q is rejected by configuration validation: %v", file, group, err)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no pii_filter example was checked; the docs check would silently pass")
	}
}

// piiFiltersInDoc finds pii_filter blocks either under models.<group> or at the
// top level of the fence.
func piiFiltersInDoc(t *testing.T, doc any) map[string]PIIFilterConfig {
	t.Helper()
	out := map[string]PIIFilterConfig{}
	load := func(group string, value any) {
		raw, err := yaml.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var cfg PIIFilterConfig
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		out[group] = cfg
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return out
	}
	if value, ok := root["pii_filter"]; ok {
		load("example", value)
	}
	if models, ok := root["models"].(map[string]any); ok {
		for name, value := range models {
			group, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if filter, ok := group["pii_filter"]; ok {
				load(name, filter)
			}
		}
	}
	return out
}

// TestPIIDocsDoNotAdvertiseRestoration requires every published mention of the
// rejected restore mode to name the validation error that rejects it, and keeps
// the supported modes consistent with validatePIIFilter.
func TestPIIDocsDoNotAdvertiseRestoration(t *testing.T) {
	for _, file := range piiDocsFiles {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if strings.Contains(text, "redact_and_restore") && !strings.Contains(text, "F-011") {
			t.Fatalf("%s names redact_and_restore without naming the F-011 validation rejection", file)
		}
		if strings.Contains(text, "restore_response") && !strings.Contains(text, "F-011") {
			t.Fatalf("%s names restore_response without naming the F-011 validation rejection", file)
		}
	}
}
