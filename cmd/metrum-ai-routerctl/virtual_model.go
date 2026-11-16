// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/metrum-ai/router/internal/router"
	"gopkg.in/yaml.v3"
)

const maxVirtualEvalReportBytes = 32 << 20
const maxVirtualEvalMatrixBytes = 2 << 20

type evalReportForVirtualModel struct {
	SchemaVersion int                        `json:"schema_version"`
	RunID         string                     `json:"run_id"`
	StartedUTC    string                     `json:"started_utc"`
	Cells         []evalReportCellForVirtual `json:"cells"`
}

type evalReportCellForVirtual struct {
	CellID      string               `json:"cell_id"`
	Mode        string               `json:"mode"`
	Provenance  map[string]any       `json:"provenance"`
	TaskResults []evalTaskForVirtual `json:"task_results"`
}

type evalTaskForVirtual struct {
	TaskID            string  `json:"task_id"`
	TargetModel       string  `json:"target_model"`
	Outcome           string  `json:"outcome"`
	InputTokens       *int    `json:"input_tokens"`
	ToolTurns         int     `json:"tool_turns"`
	CompletionLatency float64 `json:"completion_latency_ms"`
	CostUSD           float64 `json:"cost_usd"`
}

type evalMatrixForVirtual struct {
	Cells []struct {
		ID    string `yaml:"id"`
		Suite string `yaml:"suite"`
	} `yaml:"cells"`
}

type virtualGroupResponse struct {
	Revision     int64                      `json:"revision"`
	VirtualModel *router.VirtualModelConfig `json:"virtual_model"`
	Targets      []struct {
		Provider   string                            `json:"provider"`
		Model      string                            `json:"model"`
		ModelRef   string                            `json:"model_ref"`
		Weight     int                               `json:"weight"`
		Region     string                            `json:"region"`
		Tags       []string                          `json:"tags"`
		Validation *router.GroupTargetValidationSpec `json:"validation"`
	} `json:"targets"`
}

type evalTaskAggregate struct {
	attempts     int
	passes       int
	toolAttempts int
	toolPasses   int
	maxContext   int
	costs        []float64
	latencies    []float64
}

// virtualCapabilityVectors validates report provenance and derives target vectors by
// task suite. The report's target_model must resolve to exactly one configured target.
func virtualCapabilityVectors(reportBytes, matrixBytes []byte, targets []router.GroupTargetSpec) (map[int]map[string]router.TargetCapabilityVector, string, error) {
	var report evalReportForVirtualModel
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		return nil, "", errors.New("eval report is invalid JSON")
	}
	started, err := time.Parse(time.RFC3339, report.StartedUTC)
	if err != nil || report.SchemaVersion != 1 || !safeEvalIdentifier(report.RunID, 128) || len(report.Cells) == 0 {
		return nil, "", errors.New("eval report requires schema_version 1, run_id, started_utc, and cells")
	}
	var matrix evalMatrixForVirtual
	if err := yaml.Unmarshal(matrixBytes, &matrix); err != nil {
		return nil, "", errors.New("preregistered matrix is invalid YAML")
	}
	suites := map[string]string{}
	for _, cell := range matrix.Cells {
		if cell.ID != "" && cell.Suite != "" {
			suites[cell.ID] = cell.Suite
		}
	}
	if len(suites) == 0 {
		return nil, "", errors.New("preregistered matrix has no eval cells")
	}
	digest := sha256.Sum256(reportBytes)
	reportHash := hex.EncodeToString(digest[:])
	measuredAt := started.UTC().Format("2006-01-02")
	aggregates := map[int]map[string]*evalTaskAggregate{}
	for _, cell := range report.Cells {
		if cell.Mode != "dynamic-group" {
			continue
		}
		family := suites[cell.CellID]
		if !safeEvalIdentifier(family, 80) {
			return nil, "", fmt.Errorf("eval cell %q is absent from the preregistered matrix", cell.CellID)
		}
		if !reportProvenanceComplete(cell.Provenance) {
			return nil, "", fmt.Errorf("eval cell %q lacks reproducibility provenance", cell.CellID)
		}
		for _, task := range cell.TaskResults {
			if task.Outcome == "skipped" {
				continue
			}
			if task.TargetModel == "" || task.TaskID == "" || task.InputTokens == nil || *task.InputTokens < 0 || task.ToolTurns < 0 || !finiteNonnegative(task.CompletionLatency) || !finiteNonnegative(task.CostUSD) || (task.Outcome != "pass" && task.Outcome != "fail" && task.Outcome != "error") {
				return nil, "", fmt.Errorf("eval cell %q has an incomplete task result", cell.CellID)
			}
			index, matches := matchEvalTarget(task.TargetModel, targets)
			if len(matches) != 1 {
				return nil, "", fmt.Errorf("eval target %q must match exactly one group target", task.TargetModel)
			}
			if aggregates[index] == nil {
				aggregates[index] = map[string]*evalTaskAggregate{}
			}
			a := aggregates[index][family]
			if a == nil {
				a = &evalTaskAggregate{}
				aggregates[index][family] = a
			}
			a.attempts++
			if task.Outcome == "pass" {
				a.passes++
			}
			if task.ToolTurns > 0 {
				a.toolAttempts++
				if task.Outcome == "pass" {
					a.toolPasses++
				}
			}
			if *task.InputTokens > a.maxContext {
				a.maxContext = *task.InputTokens
			}
			a.costs = append(a.costs, task.CostUSD)
			a.latencies = append(a.latencies, task.CompletionLatency)
		}
	}
	if len(aggregates) == 0 {
		return nil, "", errors.New("eval report has no dynamic-group task results")
	}
	out := map[int]map[string]router.TargetCapabilityVector{}
	for i := range targets {
		out[i] = map[string]router.TargetCapabilityVector{}
	}
	for targetIndex, byFamily := range aggregates {
		for family, a := range byFamily {
			quality := float64(a.passes) / float64(a.attempts)
			meanCost := 0.0
			for _, cost := range a.costs {
				meanCost += cost
			}
			meanCost /= float64(len(a.costs))
			vector := router.TargetCapabilityVector{QualityScore: quality, ContextTokens: a.maxContext, P95LatencyMS: nearestRank95(a.latencies), MeanCostUSD: meanCost, EvalRunID: report.RunID, EvalReportSHA256: reportHash, TaskFamily: family, MeasuredAt: measuredAt}
			if a.toolAttempts > 0 {
				reliability := float64(a.toolPasses) / float64(a.toolAttempts)
				vector.ToolReliability = &reliability
			}
			out[targetIndex][family] = vector
		}
	}
	return out, reportHash, nil
}

func reportProvenanceComplete(p map[string]any) bool {
	for _, key := range []string{"router_commit", "dataset_revision"} {
		value, ok := p[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
	}
	versions, ok := p["client_versions"].(map[string]any)
	if !ok || len(versions) == 0 || p["seed"] == nil {
		return false
	}
	for _, value := range versions {
		version, valid := value.(string)
		if !valid || strings.TrimSpace(version) == "" {
			return false
		}
	}
	images, ok := p["task_image_digests"].(map[string]any)
	if !ok || len(images) == 0 {
		return false
	}
	for _, value := range images {
		digest, valid := value.(string)
		if !valid || !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
			return false
		}
	}
	return true
}

func matchEvalTarget(model string, targets []router.GroupTargetSpec) (int, []int) {
	matches := []int{}
	for i, target := range targets {
		if model == target.Model || model == target.ModelRef || model == target.Provider+"/"+target.ModelRef || model == target.Provider+"/"+target.Model {
			matches = append(matches, i)
		}
	}
	if len(matches) == 1 {
		return matches[0], matches
	}
	return -1, matches
}

func nearestRank95(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	index := int(math.Ceil(0.95*float64(len(ordered)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func safeEvalIdentifier(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			continue
		}
		return false
	}
	return true
}

func virtualImportCommand(args []string) {
	fs, api := addAdminAPIFlags("groups import-eval")
	group := fs.String("group", "", "virtual model group")
	reportPath := fs.String("report", "", "preregistered eval report JSON")
	matrixPath := fs.String("matrix", "evaluators/eval-infra/preregistered-matrix.yaml", "preregistered eval matrix YAML")
	reason := fs.String("reason", "import reproducible eval capability vectors", "audit reason")
	fs.Parse(args)
	if *group == "" || *reportPath == "" {
		die("groups import-eval requires --group and --report")
	}
	reportBytes, err := readVirtualImportFile(*reportPath, maxVirtualEvalReportBytes)
	if err != nil {
		die("read eval report: %v", err)
	}
	matrixBytes, err := readVirtualImportFile(*matrixPath, maxVirtualEvalMatrixBytes)
	if err != nil {
		die("read eval matrix: %v", err)
	}
	client := mustAdminAPIClient(api)
	path := "/admin/v1/config/groups/" + url.PathEscape(*group)
	body, _, err := client.Do(http.MethodGet, path, nil, nil)
	if err != nil {
		dieAdmin(err)
	}
	var current virtualGroupResponse
	if err := json.Unmarshal(body, &current); err != nil {
		die("decode virtual model group response")
	}
	if current.VirtualModel == nil {
		die("group %q is not a virtual model", *group)
	}
	if len(current.Targets) == 0 {
		die("virtual model group has no targets")
	}
	targets := make([]router.GroupTargetSpec, len(current.Targets))
	for i, target := range current.Targets {
		targets[i] = router.GroupTargetSpec{Provider: target.Provider, Model: target.Model, ModelRef: target.ModelRef, Weight: target.Weight, Region: target.Region, Tags: target.Tags, Validation: target.Validation}
	}
	vectors, reportHash, err := virtualCapabilityVectors(reportBytes, matrixBytes, targets)
	if err != nil {
		die("eval report rejected: %v", err)
	}
	families := map[string]bool{}
	for _, byFamily := range vectors {
		for family := range byFamily {
			families[family] = true
		}
	}
	for i, byFamily := range vectors {
		validation := targets[i].Validation
		if validation == nil && len(byFamily) > 0 {
			validation = &router.GroupTargetValidationSpec{}
		}
		if validation == nil {
			continue
		}
		if validation.Capabilities == nil {
			validation.Capabilities = map[string]router.TargetCapabilityVector{}
		}
		for family := range families {
			delete(validation.Capabilities, family)
		}
		for family, vector := range byFamily {
			validation.Capabilities[family] = vector
		}
		if len(byFamily) > 0 {
			validation.Status = "passed"
			validation.ValidatedAt = time.Now().UTC().Format("2006-01-02")
			validation.Harness = "eval-infra"
			validation.Workload = "eval-infra"
			validation.Notes = "Imported report sha256:" + reportHash
		}
		targets[i].Validation = validation
	}
	request := map[string]any{"targets": targets, "reason": *reason}
	headers := map[string]string{"If-Match": fmt.Sprintf("\"%d\"", current.Revision), "Idempotency-Key": "eval-" + reportHash[:32]}
	result, _, _, err := client.DoWithHeaders(http.MethodPut, path+"/targets", request, headers)
	if err != nil {
		dieAdmin(err)
	}
	writeRawJSON(result)
}

func readVirtualImportFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("file must be regular, non-empty and at most %d bytes", maxBytes)
	}
	return os.ReadFile(path)
}
