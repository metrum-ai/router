// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import "testing"

func TestDetectorConfigValidatesSegmentOptInAndErrorSemantics(t *testing.T) {
	base := DetectorConfig{ID: "remote", URL: "https://detector.example/v1/detect", AllowHosts: []string{"detector.example"}, Mode: "report", TimeoutMS: 500, OnError: "fallback", SampleRate: 1}
	base.IncludeSegments = []string{"tool_result", "document"}
	if err := validateDetectorConfigs([]DetectorConfig{base}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	base.IncludeSegments = []string{"prompt"}
	if err := validateDetectorConfigs([]DetectorConfig{base}); err == nil {
		t.Fatal("prompt segment accepted")
	}
	base.IncludeSegments = nil
	base.OnError = "ignore"
	if err := validateDetectorConfigs([]DetectorConfig{base}); err == nil {
		t.Fatal("legacy detector error mode accepted")
	}
}

func TestConfiguredDetectorDefaultsToReport(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Detectors: []DetectorConfig{{ID: "remote", URL: "https://detector.example/v1/detect", AllowHosts: []string{"detector.example"}}}}}
	cfg.setDefaults()
	if got := cfg.Server.Detectors[0].Mode; got != "report" {
		t.Fatalf("configured detector default mode=%q, want report", got)
	}
}
