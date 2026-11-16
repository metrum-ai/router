// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metrum-ai/router/internal/router"
)

func TestVirtualCapabilityVectorsFromReproducibleReport(t *testing.T) {
	report := `{"schema_version":1,"run_id":"run-1","started_utc":"2026-11-16T00:00:00Z","cells":[{"cell_id":"coding-dynamic","mode":"dynamic-group","provenance":{"router_commit":"abc","dataset_revision":"def","seed":7,"client_versions":{"python":"3.12"},"task_image_digests":{"sandbox":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},"task_results":[{"task_id":"case-1","target_model":"provider/model-a","outcome":"pass","input_tokens":100,"tool_turns":1,"completion_latency_ms":100,"cost_usd":0.01},{"task_id":"case-2","target_model":"provider/model-a","outcome":"fail","input_tokens":200,"tool_turns":0,"completion_latency_ms":200,"cost_usd":0.03}]}]}`
	matrix := "version: 1\ncells:\n  - id: coding-dynamic\n    suite: coding\n"
	targets := []router.GroupTargetSpec{{Provider: "provider", ModelRef: "model-a"}}
	vectors, digest, err := virtualCapabilityVectors([]byte(report), []byte(matrix), targets)
	if err != nil {
		t.Fatal(err)
	}
	v := vectors[0]["coding"]
	if len(digest) != 64 || v.QualityScore != 0.5 || v.ToolReliability == nil || *v.ToolReliability != 1 || v.ContextTokens != 200 || v.P95LatencyMS != 200 || v.MeanCostUSD != 0.02 || v.EvalRunID != "run-1" || v.EvalReportSHA256 != digest {
		t.Fatalf("unexpected derived vector: %#v", v)
	}
	bad := strings.Replace(report, `"dataset_revision":"def",`, "", 1)
	if _, _, err := virtualCapabilityVectors([]byte(bad), []byte(matrix), targets); err == nil {
		t.Fatal("report without dataset revision was accepted")
	}
	ambiguous := append(targets, router.GroupTargetSpec{Provider: "other", ModelRef: "provider/model-a"})
	if _, _, err := virtualCapabilityVectors([]byte(report), []byte(matrix), ambiguous); err == nil {
		t.Fatal("ambiguous target identity was accepted")
	}
}

func TestVirtualCapabilityImporterAcceptsStoredEvalInfrastructureReport(t *testing.T) {
	root := filepath.Join("..", "..")
	report, err := os.ReadFile(filepath.Join(root, "docs/evaluation-reports/inspect/2026-10-27-issue-330/report.json"))
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := os.ReadFile(filepath.Join(root, "evaluators/eval-infra/preregistered-matrix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	targets := []router.GroupTargetSpec{
		{Provider: "fireworks", ModelRef: "accounts/fireworks/models/deepseek-v4p1-flash"},
		{Provider: "fireworks", ModelRef: "accounts/fireworks/models/deepseek-v4p1-flash-1017"},
	}
	vectors, _, err := virtualCapabilityVectors(report, matrix, targets)
	if err != nil {
		t.Fatal(err)
	}
	_, first := vectors[0]["humaneval"]
	_, second := vectors[1]["humaneval"]
	if len(vectors) != 2 || !first || !second {
		t.Fatalf("stored report did not produce vectors for both evaluated targets: %#v", vectors)
	}
}
