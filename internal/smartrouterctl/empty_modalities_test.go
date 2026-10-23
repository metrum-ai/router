// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0
package smartrouterctl

import (
	"github.com/metrum-ai/router/internal/router"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCallerConfigWriterOmitsEmptyLegacyModalities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &router.Config{Models: map[string]router.ModelGroup{"text": {Strategy: "static", Targets: []router.Target{{Provider: "p", Model: "m", Modalities: []string{}, InputModalities: []string{"text"}}}}}}
	if _, err := WriteConfigAtomic(path, cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), " modalities:") {
		t.Fatal("CLI writer emitted empty legacy modalities")
	}
	loaded, err := LoadConfigRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Models["text"].Targets[0]; len(got.Modalities) != 0 || len(got.InputModalities) != 1 || got.InputModalities[0] != "text" {
		t.Fatalf("capability lost: %#v", got)
	}
}
