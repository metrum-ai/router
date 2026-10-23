// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package router

import (
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestEmptyLegacyModalitiesAreUnset(t *testing.T) {
	for _, tc := range []struct {
		name, raw   string
		text, image bool
	}{
		{"absent", "model: m", true, false},
		{"empty", "model: m\nmodalities: []", true, false},
		{"empty-with-explicit-input", "modalities: []\ninput_modalities: [text, image]", true, true},
		{"nonempty-legacy-override", "modalities: [image]\ninput_modalities: [text]", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var target Target
			if err := yaml.Unmarshal([]byte(tc.raw), &target); err != nil {
				t.Fatal(err)
			}
			for modality, want := range map[string]bool{"text": tc.text, "image": tc.image} {
				if got := targetSupportsInputModalities(target, []string{modality}); got != want {
					t.Fatalf("%s got %t want %t", modality, got, want)
				}
			}
			body, err := yaml.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(target.Modalities) == 0 && strings.Contains(string(body), "\nmodalities:") {
				t.Fatalf("empty legacy field emitted: %s", body)
			}
			var roundtrip Target
			if err := yaml.Unmarshal(body, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if targetSupportsInputModalities(roundtrip, []string{"text"}) != tc.text {
				t.Fatal("roundtrip changed capability")
			}
		})
	}
}
