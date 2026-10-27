// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

// Shadow runs for the Admin API (metrum-ai/router#395 §5).
//
// A shadow run puts one candidate catalog target in shadow against a model
// group's served traffic: sampled requests are replayed to the candidate
// after the serving decision, off the caller's critical path, and only
// aggregate counters (requests, errors, latency, tokens, estimated cost) are
// recorded on the shared run row. The comparison endpoint reads those
// counters against the group's served baseline from the existing usage
// telemetry. Promotion is a §2 copy-on-write group write whose audit event
// links the shadow run.
//
// The run registry lives in the config database (phase 9) so every replica
// shadows the same candidate at the same sample rate; lifecycle changes bump
// the runtime revision and converge through the existing revision
// poller/notifier, the same propagation channel as config writes. Shadow
// replays never write request_usage rows, never carry caller identity, and
// never log request or response content.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used Non-cryptographic sampling of shadow replays only; no security-sensitive or identifier generation depends on it (metrum-ai/router#412).
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const (
	shadowRunStatusActive    = "active"
	shadowRunStatusStopped   = "stopped"
	shadowRunStatusExhausted = "exhausted"
	shadowRunStatusPromoted  = "promoted"

	// maxShadowInFlight bounds concurrent shadow replays per replica so a
	// slow candidate can never consume the caller-facing request budget.
	maxShadowInFlight = 8
	// maxShadowResponseBytes caps shadow response reads.
	maxShadowResponseBytes = 4 << 20
	// maxShadowSampleRate bounds sample_rate.
	maxShadowSampleRate = 1.0
)

// shadowRunRow is the GORM projection for router_config_shadow_runs.
type shadowRunRow struct {
	RunID                 string  `gorm:"column:run_id;primaryKey"`
	RuntimeScope          string  `gorm:"column:runtime_scope"`
	GroupName             string  `gorm:"column:group_name"`
	ProviderName          string  `gorm:"column:provider_name"`
	ModelRef              string  `gorm:"column:model_ref"`
	Model                 string  `gorm:"column:model"`
	SampleRate            float64 `gorm:"column:sample_rate"`
	ValidationStatus      string  `gorm:"column:validation_status"`
	ValidationWorkload    string  `gorm:"column:validation_workload"`
	ValidationValidatedAt string  `gorm:"column:validation_validated_at"`
	MaxRequests           int64   `gorm:"column:max_requests"`
	DurationSeconds       int64   `gorm:"column:duration_seconds"`
	CostCapUSD            float64 `gorm:"column:cost_cap_usd"`
	Status                string  `gorm:"column:status"`
	CreatedBy             string  `gorm:"column:created_by"`
	CreatedAt             string  `gorm:"column:created_at"`
	StoppedAt             string  `gorm:"column:stopped_at"`
	PromotedSetID         string  `gorm:"column:promoted_set_id"`
	Requests              int64   `gorm:"column:requests"`
	Errors                int64   `gorm:"column:errors"`
	LatencyMSTotal        float64 `gorm:"column:latency_ms_total"`
	PromptTokens          int64   `gorm:"column:prompt_tokens"`
	CompletionTokens      int64   `gorm:"column:completion_tokens"`
	CostUSD               float64 `gorm:"column:cost_usd"`
}

func (shadowRunRow) TableName() string { return configControlPlaneShadowRunsTable }

// ShadowRun is the API view of one shadow run.
type ShadowRun struct {
	RunID           string  `json:"run_id"`
	Group           string  `json:"group"`
	Provider        string  `json:"provider"`
	ModelRef        string  `json:"model_ref,omitempty"`
	Model           string  `json:"model,omitempty"`
	SampleRate      float64 `json:"sample_rate"`
	MaxRequests     int64   `json:"max_requests,omitempty"`
	DurationSeconds int64   `json:"duration_seconds,omitempty"`
	CostCapUSD      float64 `json:"cost_cap_usd,omitempty"`
	Status          string  `json:"status"`
	CreatedBy       string  `json:"created_by"`
	CreatedAt       string  `json:"created_at"`
	StoppedAt       string  `json:"stopped_at,omitempty"`
	PromotedSetID   string  `json:"promoted_set_id,omitempty"`
}

func shadowRunView(row shadowRunRow) ShadowRun {
	return ShadowRun{
		RunID: row.RunID, Group: row.GroupName, Provider: row.ProviderName,
		ModelRef: row.ModelRef, Model: row.Model, SampleRate: row.SampleRate,
		MaxRequests: row.MaxRequests, DurationSeconds: row.DurationSeconds,
		CostCapUSD: row.CostCapUSD, Status: row.Status, CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt, StoppedAt: row.StoppedAt, PromotedSetID: row.PromotedSetID,
	}
}

// ShadowStartRequest is the POST shadow body.
type ShadowStartRequest struct {
	Provider        string                     `json:"provider"`
	Model           string                     `json:"model,omitempty"`
	ModelRef        string                     `json:"model_ref,omitempty"`
	SampleRate      float64                    `json:"sample_rate"`
	MaxRequests     int64                      `json:"max_requests,omitempty"`
	DurationSeconds int64                      `json:"duration_seconds,omitempty"`
	CostCapUSD      float64                    `json:"cost_cap_usd,omitempty"`
	Reason          string                     `json:"reason,omitempty"`
	Validation      *GroupTargetValidationSpec `json:"validation,omitempty"`
}

// validateShadowStartRequest validates the sampling envelope. Candidate
// catalog/contract eligibility is checked separately by the caller.
func validateShadowStartRequest(req ShadowStartRequest) *ConfigAdminError {
	if strings.TrimSpace(req.Provider) == "" {
		return configAdminError(400, "config-invalid-request", "provider is required")
	}
	if strings.TrimSpace(req.ModelRef) == "" && strings.TrimSpace(req.Model) == "" {
		return configAdminError(400, "config-invalid-request", "model or model_ref is required")
	}
	if req.SampleRate <= 0 || req.SampleRate > maxShadowSampleRate {
		return configAdminError(400, "config-invalid-request", "sample_rate must be in (0, 1]")
	}
	if req.MaxRequests < 0 || req.DurationSeconds < 0 || req.CostCapUSD < 0 {
		return configAdminError(400, "config-invalid-request", "max_requests, duration_seconds and cost_cap_usd cannot be negative")
	}
	if req.MaxRequests == 0 && req.DurationSeconds == 0 && req.CostCapUSD <= 0 {
		return configAdminError(400, "config-invalid-request", "set at least one bound: max_requests, duration_seconds, or cost_cap_usd")
	}
	return nil
}

// bumpRevisionOnlyTx bumps the runtime revision without changing the active
// set, so replicas reload (including shadow state) through the existing
// propagation channel.
func bumpRevisionOnlyTx(tx *gorm.DB, runtimeScope string) (int64, error) {
	var setID string
	if err := tx.Raw(`SELECT active_set_id FROM router_config_runtime WHERE runtime_scope = ?`, runtimeScope).Row().Scan(&setID); err != nil {
		return 0, fmt.Errorf("load runtime row: %w", err)
	}
	switch tx.Dialector.Name() {
	case "sqlite":
		if err := tx.Exec(`UPDATE router_config_runtime SET config_revision = config_revision + 1 WHERE runtime_scope = ?`, runtimeScope).Error; err != nil {
			return 0, err
		}
	case "postgres":
		if err := tx.Exec(`UPDATE router_config_runtime SET config_revision = router_config_runtime.config_revision + 1 WHERE runtime_scope = ?`, runtimeScope).Error; err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("unsupported control-plane database driver %q", tx.Dialector.Name())
	}
	return currentRevisionForScope(tx, runtimeScope)
}

// StartGroupShadow validates and registers a shadow run for the group. The
// candidate must resolve against the active catalog and satisfy the group
// contract for the shapes the group promises — the same eligibility bar as a
// served target.
func StartGroupShadow(db *gorm.DB, runtimeScope, group string, req ShadowStartRequest, actor string, policy GroupWritePolicy, now time.Time) (*ShadowRun, error) {
	if db == nil {
		return nil, configAdminError(409, "config_source_mode_file", "shadow runs require database mode (server.config_source.mode: database)")
	}
	if err := validateShadowStartRequest(req); err != nil {
		return nil, err
	}
	group = strings.TrimSpace(group)
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return nil, errors.New("config actor is required")
	}
	var result *ShadowRun
	err := withSQLiteBusyRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := lockConfigRuntimeTx(tx, runtimeScope); err != nil {
				return err
			}
			if err := ensureConfigShadowSchema(tx); err != nil {
				return err
			}
			state, _, err := loadConfigAdminRuntime(tx, runtimeScope)
			if err != nil {
				return err
			}
			raw, err := ExportCanonicalYAML(tx, state.ActiveSetID)
			if err != nil {
				return err
			}
			var current Config
			if err := yaml.Unmarshal(raw, &current); err != nil {
				return fmt.Errorf("parse active canonical YAML: %w", err)
			}
			current.setDefaults()
			existing, ok := current.Models[group]
			if !ok {
				return configAdminError(404, "group-not-found", "model group is not in the active configuration")
			}
			// Candidate eligibility: catalog resolution plus contract checks,
			// exactly as for a served target.
			spec := GroupTargetSpec{Provider: req.Provider, Model: req.Model, ModelRef: req.ModelRef, Validation: req.Validation}
			reports, _ := validateGroupTargetSpecs(&current, group, existing, []GroupTargetSpec{spec}, policy, now)
			if len(reports) != 1 || !reports[0].Eligible {
				return &ConfigAdminError{Status: 422, Type: "target-ineligible", Message: "the shadow candidate fails catalog or contract eligibility", Reports: reports}
			}
			var active int64
			if err := tx.Table(configControlPlaneShadowRunsTable).
				Where("runtime_scope = ? AND group_name = ? AND status = ?", runtimeScope, group, shadowRunStatusActive).
				Count(&active).Error; err != nil {
				return err
			}
			if active > 0 {
				return configAdminError(409, "shadow-already-active", "the group already has an active shadow run; stop it first")
			}
			runID := fmt.Sprintf("shr-%d", now.UTC().UnixNano())
			resolved, _ := current.resolveTarget(group, Target{Provider: req.Provider, Model: req.Model, ModelRef: req.ModelRef})
			var validationStatus, validationWorkload, validationValidatedAt string
			if req.Validation != nil {
				validationStatus = strings.TrimSpace(req.Validation.Status)
				validationWorkload = strings.TrimSpace(req.Validation.Workload)
				validationValidatedAt = strings.TrimSpace(req.Validation.ValidatedAt)
			}
			row := shadowRunRow{
				RunID: runID, RuntimeScope: runtimeScope, GroupName: group,
				ProviderName: req.Provider, ModelRef: req.ModelRef, Model: resolved.Model,
				SampleRate: req.SampleRate, ValidationStatus: validationStatus,
				ValidationWorkload: validationWorkload, ValidationValidatedAt: validationValidatedAt,
				MaxRequests:     req.MaxRequests,
				DurationSeconds: req.DurationSeconds, CostCapUSD: req.CostCapUSD,
				Status: shadowRunStatusActive, CreatedBy: actor,
				CreatedAt: now.UTC().Format(time.RFC3339Nano),
			}
			if err := tx.Exec(
				`INSERT INTO router_config_shadow_runs (run_id, runtime_scope, group_name, provider_name, model_ref, model, sample_rate, validation_status, validation_workload, validation_validated_at, max_requests, duration_seconds, cost_cap_usd, status, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				row.RunID, row.RuntimeScope, row.GroupName, row.ProviderName, row.ModelRef, row.Model,
				row.SampleRate, row.ValidationStatus, row.ValidationWorkload, row.ValidationValidatedAt, row.MaxRequests, row.DurationSeconds, row.CostCapUSD, row.Status, row.CreatedBy, row.CreatedAt,
			).Error; err != nil {
				if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(strings.ToLower(err.Error()), "unique") {
					return configAdminError(409, "shadow-already-active", "the group already has an active shadow run; stop it first")
				}
				return fmt.Errorf("insert shadow run: %w", err)
			}
			revision, err := bumpRevisionOnlyTx(tx, runtimeScope)
			if err != nil {
				return err
			}
			if _, err := insertConfigChangeEventTx(tx, runtimeScope, state.ActiveSetID, configActionShadowStart, actor, revision, configChangeEventMeta{
				GroupName: group,
				Reason:    req.Reason,
				Detail:    "shadow_run=" + runID,
			}, now); err != nil {
				return err
			}
			view := shadowRunView(row)
			result = &view
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// StopGroupShadow stops the group's active shadow run.
func StopGroupShadow(db *gorm.DB, runtimeScope, group, actor, reason string, now time.Time) (*ShadowRun, error) {
	if db == nil {
		return nil, configAdminError(409, "config_source_mode_file", "shadow runs require database mode (server.config_source.mode: database)")
	}
	var result *ShadowRun
	err := withSQLiteBusyRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := lockConfigRuntimeTx(tx, runtimeScope); err != nil {
				return err
			}
			if err := ensureConfigShadowSchema(tx); err != nil {
				return err
			}
			state, _, err := loadConfigAdminRuntime(tx, runtimeScope)
			if err != nil {
				return err
			}
			res := tx.Exec(
				`UPDATE router_config_shadow_runs SET status = ?, stopped_at = ? WHERE runtime_scope = ? AND group_name = ? AND status = ?`,
				shadowRunStatusStopped, now.UTC().Format(time.RFC3339Nano), runtimeScope, group, shadowRunStatusActive,
			)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return configAdminError(404, "shadow-not-found", "the group has no active shadow run")
			}
			revision, err := bumpRevisionOnlyTx(tx, runtimeScope)
			if err != nil {
				return err
			}
			if _, err := insertConfigChangeEventTx(tx, runtimeScope, state.ActiveSetID, configActionShadowStop, actor, revision, configChangeEventMeta{
				GroupName: group,
				Reason:    reason,
			}, now); err != nil {
				return err
			}
			run, err := latestShadowRun(tx, runtimeScope, group)
			if err != nil {
				return err
			}
			view := shadowRunView(run)
			result = &view
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// PromoteGroupShadow promotes the group's latest shadow candidate through the
// §2 copy-on-write write path: the candidate becomes the group's first
// target and the previous targets remain as fallback (duplicates removed).
// The audit event links the shadow run via detail=shadow_run=<run_id>, and
// the run is marked promoted with the new set id.
func PromoteGroupShadow(db *gorm.DB, runtimeScope, group, actor, reason string, force bool, policy GroupWritePolicy, now time.Time) (*GroupTargetsWriteResult, string, error) {
	if db == nil {
		return nil, "", configAdminError(409, "config_source_mode_file", "shadow runs require database mode (server.config_source.mode: database)")
	}
	// Read the promotable run first (active or stopped, never promoted).
	var run shadowRunRow
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ensureConfigShadowSchema(tx); err != nil {
			return err
		}
		var err error
		run, err = latestShadowRun(tx, runtimeScope, group)
		return err
	}); err != nil {
		return nil, "", err
	}
	if run.RunID == "" {
		return nil, "", configAdminError(404, "shadow-not-found", "the group has no shadow run to promote")
	}
	if run.Status == shadowRunStatusPromoted {
		// Idempotent promotion replay.
		return &GroupTargetsWriteResult{
			Group: group, SetID: run.PromotedSetID, Replayed: true,
			Targets: []GroupTargetReport{},
			Diff:    GroupTargetsDiff{Added: []string{}, Removed: []string{}, Reordered: []string{}},
		}, run.RunID, nil
	}

	// Build the promoted target list: candidate first, previous targets after.
	cfg, err := LoadActiveCanonicalConfig(db, runtimeScope)
	if err != nil {
		return nil, "", err
	}
	existing, ok := cfg.Models[group]
	if !ok {
		return nil, "", configAdminError(404, "group-not-found", "model group is not in the active configuration")
	}
	candidate := GroupTargetSpec{Provider: run.ProviderName, Model: run.Model, ModelRef: run.ModelRef}
	if run.ValidationStatus != "" {
		candidate.Validation = &GroupTargetValidationSpec{
			Status: run.ValidationStatus, Workload: run.ValidationWorkload, ValidatedAt: run.ValidationValidatedAt,
		}
	}
	targets := []GroupTargetSpec{candidate}
	for _, spec := range groupTargetSpecsFromGroup(existing) {
		if spec.Provider == candidate.Provider && (spec.ModelRef == candidate.ModelRef && spec.Model == candidate.Model) {
			continue
		}
		targets = append(targets, spec)
	}
	result, err := WriteGroupTargets(db, runtimeScope, GroupTargetsWriteRequest{
		Group: group, Targets: targets, Reason: reason, Force: force,
		Action: configActionShadowPromote,
	}, actor, policy, now)
	if err != nil {
		return nil, "", err
	}
	// Mark the run promoted and link the new set; stamp the shadow_run link
	// onto the change event's detail for the audit chain.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ensureConfigShadowSchema(tx); err != nil {
			return err
		}
		if err := tx.Exec(
			`UPDATE router_config_shadow_runs SET status = ?, stopped_at = CASE WHEN stopped_at = '' THEN ? ELSE stopped_at END, promoted_set_id = ? WHERE run_id = ? AND status != ?`,
			shadowRunStatusPromoted, now.UTC().Format(time.RFC3339Nano), result.SetID, run.RunID, shadowRunStatusPromoted,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(
			`UPDATE router_config_change_events SET detail = ? WHERE runtime_scope = ? AND config_set_id = ? AND action = ?`,
			"shadow_run="+run.RunID, runtimeScope, result.SetID, configActionShadowPromote,
		).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, "", err
	}
	return result, run.RunID, nil
}

func latestShadowRun(tx *gorm.DB, runtimeScope, group string) (shadowRunRow, error) {
	var rows []shadowRunRow
	if err := tx.Where("runtime_scope = ? AND group_name = ?", runtimeScope, group).
		Order("created_at DESC").Limit(1).Find(&rows).Error; err != nil {
		return shadowRunRow{}, fmt.Errorf("load shadow run: %w", err)
	}
	if len(rows) == 0 {
		return shadowRunRow{}, nil
	}
	return rows[0], nil
}

// shadowAggregateStats is one side of the comparison.
type shadowAggregateStats struct {
	Requests          int64   `json:"requests"`
	Errors            int64   `json:"errors"`
	ErrorRate         float64 `json:"error_rate"`
	AvgLatencyMS      float64 `json:"avg_latency_ms"`
	InputTokens       int64   `json:"input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	CostPerRequestUSD float64 `json:"cost_per_request_usd"`
}

// ShadowComparison is the GET shadow response body.
type ShadowComparison struct {
	Group     string               `json:"group"`
	Run       ShadowRun            `json:"run"`
	Window    map[string]string    `json:"window"`
	Served    shadowAggregateStats `json:"served"`
	Shadow    shadowAggregateStats `json:"shadow"`
	Available bool                 `json:"baseline_available"`
}

func shadowStatsFromRow(row shadowRunRow) shadowAggregateStats {
	stats := shadowAggregateStats{
		Requests: row.Requests, Errors: row.Errors,
		InputTokens: row.PromptTokens, OutputTokens: row.CompletionTokens,
		CostUSD: row.CostUSD,
	}
	if row.Requests > 0 {
		stats.ErrorRate = float64(row.Errors) / float64(row.Requests)
		stats.AvgLatencyMS = row.LatencyMSTotal / float64(row.Requests)
		stats.CostPerRequestUSD = row.CostUSD / float64(row.Requests)
	}
	return stats
}

// GetGroupShadow reads the latest shadow run and its comparison. Baseline
// aggregates come from the usage store when available.
func GetGroupShadow(configDB *gorm.DB, usageDB *gorm.DB, runtimeScope, group string, now time.Time) (*ShadowComparison, error) {
	if configDB == nil {
		return nil, configAdminError(409, "config_source_mode_file", "shadow runs require database mode (server.config_source.mode: database)")
	}
	run, err := latestShadowRun(configDB, runtimeScope, group)
	if err != nil {
		return nil, err
	}
	if run.RunID == "" {
		return nil, configAdminError(404, "shadow-not-found", "the group has no shadow run")
	}
	from := run.CreatedAt
	to := now.UTC().Format(time.RFC3339Nano)
	if run.StoppedAt != "" {
		to = run.StoppedAt
	}
	comparison := &ShadowComparison{
		Group:  group,
		Run:    shadowRunView(run),
		Window: map[string]string{"from": from, "to": to},
		Shadow: shadowStatsFromRow(run),
	}
	if usageDB != nil {
		var baseline struct {
			Requests     int64   `gorm:"column:requests"`
			Errors       int64   `gorm:"column:errors"`
			AvgLatencyMS float64 `gorm:"column:avg_latency_ms"`
			InputTokens  int64   `gorm:"column:input_tokens"`
			OutputTokens int64   `gorm:"column:output_tokens"`
			CostUSD      float64 `gorm:"column:cost_usd"`
		}
		err := usageDB.Table("request_usage").
			Select("COUNT(*) AS requests, COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0) AS errors, COALESCE(AVG(latency_ms), 0) AS avg_latency_ms, COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(total_cost_usd), 0) AS cost_usd").
			Where("resolved_group = ? AND ts >= ? AND ts <= ?", group, from, to).
			Scan(&baseline).Error
		if err == nil {
			comparison.Available = true
			comparison.Served = shadowAggregateStats{
				Requests: baseline.Requests, Errors: baseline.Errors,
				AvgLatencyMS: baseline.AvgLatencyMS, InputTokens: baseline.InputTokens,
				OutputTokens: baseline.OutputTokens, CostUSD: baseline.CostUSD,
			}
			if baseline.Requests > 0 {
				comparison.Served.ErrorRate = float64(baseline.Errors) / float64(baseline.Requests)
				comparison.Served.CostPerRequestUSD = baseline.CostUSD / float64(baseline.Requests)
			}
		}
	}
	return comparison, nil
}

// recordShadowObservation atomically folds one shadow replay outcome into the
// run's aggregate counters and exhausts the run when a configured bound is
// crossed. Called off the caller's critical path.
func recordShadowObservation(db *gorm.DB, runID string, failed bool, latencyMS float64, promptTokens, completionTokens int64, costUSD float64) {
	if db == nil || runID == "" {
		return
	}
	errFlag := 0
	if failed {
		errFlag = 1
	}
	_ = withSQLiteBusyRetry(func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(
				`UPDATE router_config_shadow_runs SET requests = requests + 1, errors = errors + ?, latency_ms_total = latency_ms_total + ?, prompt_tokens = prompt_tokens + ?, completion_tokens = completion_tokens + ?, cost_usd = cost_usd + ? WHERE run_id = ? AND status = ?`,
				errFlag, latencyMS, promptTokens, completionTokens, costUSD, runID, shadowRunStatusActive,
			).Error; err != nil {
				return err
			}
			// Lazy bound enforcement: read the row and exhaust the run once a
			// configured bound is crossed. The guarded status update performs
			// the transition at most once.
			var row shadowRunRow
			if err := tx.Where("run_id = ?", runID).First(&row).Error; err != nil {
				return nil // run vanished; observation already counted best-effort
			}
			if row.Status != shadowRunStatusActive {
				return nil
			}
			exhausted := false
			if row.MaxRequests > 0 && row.Requests >= row.MaxRequests {
				exhausted = true
			}
			if row.CostCapUSD > 0 && row.CostUSD >= row.CostCapUSD {
				exhausted = true
			}
			if row.DurationSeconds > 0 {
				if created, err := time.Parse(time.RFC3339Nano, row.CreatedAt); err == nil && time.Now().UTC().After(created.Add(time.Duration(row.DurationSeconds)*time.Second)) {
					exhausted = true
				}
			}
			if !exhausted {
				return nil
			}
			return tx.Exec(
				`UPDATE router_config_shadow_runs SET status = ?, stopped_at = ? WHERE run_id = ? AND status = ?`,
				shadowRunStatusExhausted, time.Now().UTC().Format(time.RFC3339Nano), runID, shadowRunStatusActive,
			).Error
		})
	})
}

// shadowRunState is the serving-path view of an active run.
type shadowRunState struct {
	runID     string
	group     string
	provider  string
	modelRef  string
	model     string
	rate      float64
	notAfter  time.Time // zero when unbounded
	remaining int64     // max_requests snapshot; 0 = unbounded
	costCap   float64
	costSoFar float64
}

// shadowRuntime holds the active shadow runs by group for this replica.
type shadowRuntime struct {
	runs  atomic.Pointer[map[string]*shadowRunState]
	slots chan struct{}
}

func newShadowRuntime() *shadowRuntime {
	rt := &shadowRuntime{slots: make(chan struct{}, maxShadowInFlight)}
	rt.runs.Store(&map[string]*shadowRunState{})
	return rt
}

// loadShadowRunsFromDB refreshes the in-memory active-run map. Called on
// startup and whenever the config revision changes.
func (s *Service) loadShadowRunsFromDB() {
	if s == nil || s.configControlPlane == nil || s.shadows == nil {
		return
	}
	scope := strings.TrimSpace(s.cfg.Server.ConfigSource.RuntimeScope)
	var rows []shadowRunRow
	if err := s.configControlPlane.Where("runtime_scope = ? AND status = ?", scope, shadowRunStatusActive).Find(&rows).Error; err != nil {
		return
	}
	next := map[string]*shadowRunState{}
	now := time.Now().UTC()
	for _, row := range rows {
		state := &shadowRunState{
			runID: row.RunID, group: row.GroupName, provider: row.ProviderName,
			modelRef: row.ModelRef, model: row.Model, rate: row.SampleRate,
			remaining: row.MaxRequests, costCap: row.CostCapUSD, costSoFar: row.CostUSD,
		}
		if row.DurationSeconds > 0 {
			created, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
			if err != nil {
				continue
			}
			state.notAfter = created.Add(time.Duration(row.DurationSeconds) * time.Second)
			if now.After(state.notAfter) {
				continue
			}
		}
		if state.remaining > 0 {
			state.remaining -= row.Requests
			if state.remaining <= 0 {
				continue
			}
		}
		if state.costCap > 0 && state.costSoFar >= state.costCap {
			continue
		}
		next[row.GroupName] = state
	}
	s.shadows.runs.Store(&next)
}

// maybeShadowRequest samples and fires a shadow replay for the group, off the
// caller's critical path. It never blocks or fails the caller's request.
func (s *Service) maybeShadowRequest(groupName, callerDialect string, body []byte) {
	if s == nil || s.shadows == nil {
		return
	}
	runs := s.shadows.runs.Load()
	if runs == nil {
		return
	}
	run := (*runs)[groupName]
	if run == nil {
		return
	}
	if !run.notAfter.IsZero() && time.Now().UTC().After(run.notAfter) {
		return
	}
	if run.rate < 1.0 && rand.Float64() >= run.rate {
		return
	}
	select {
	case s.shadows.slots <- struct{}{}:
	default:
		return // replica-local shadow budget exhausted; drop silently
	}
	go func() {
		defer func() { <-s.shadows.slots }()
		defer func() { _ = recover() }() // shadow must never crash the router
		s.executeShadowRequest(run, callerDialect, body)
	}()
}

// executeShadowRequest replays one request body against the shadow candidate
// using the same encoding, endpoint, and credential handling as the serving
// path, then records aggregate counters. Streaming is disabled for replays.
func (s *Service) executeShadowRequest(run *shadowRunState, callerDialect string, body []byte) {
	started := time.Now()
	failed := true
	var promptTokens, completionTokens int64
	var costUSD float64
	defer func() {
		recordShadowObservation(s.configControlPlane, run.runID, failed, float64(time.Since(started).Milliseconds()), promptTokens, completionTokens, costUSD)
	}()

	req, err := decodeRequest(callerDialect, body, nil)
	if err != nil {
		return
	}
	shadowReq := *req
	shadowReq.Stream = false
	cfg := s.configSnapshot.Load()
	if cfg == nil {
		cfg = s.cfg
	}
	if cfg == nil {
		return
	}
	target, err := cfg.resolveTarget(run.group, Target{Provider: run.provider, Model: run.model, ModelRef: run.modelRef})
	if err != nil {
		return
	}
	provider, ok := cfg.Provider[target.Provider]
	if !ok || isBedrock(provider) {
		return
	}
	outDialect := targetDialect(provider, target)
	var upReqBody []byte
	switch {
	case requestShapePassthrough(callerDialect, outDialect, &shadowReq):
		upReqBody, err = encodeToolPassthrough(outDialect, target.Model, &shadowReq, target)
	case isResponsesToChatBridge(callerDialect, outDialect, target), isChatToResponsesBridge(callerDialect, outDialect, target), anthropicStreamBridge(callerDialect, outDialect):
		// Legacy bridge paths are stateful; shadow replays skip them rather
		// than recording misleading failures.
		return
	default:
		upReqBody, err = encodeUpstreamForTarget(outDialect, target.Model, &shadowReq, target)
	}
	if err != nil {
		return
	}
	endpoint, err := upstreamEndpoint(provider.BaseURL, outDialect, target)
	if err != nil {
		return
	}
	timeoutMS := s.attemptTimeoutMS(run.group, target)
	if timeoutMS <= 0 || timeoutMS > 30000 {
		timeoutMS = 30000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(upReqBody))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "metrum-ai-router-shadow/"+strings.TrimSpace(run.runID))
	for name, value := range provider.Headers {
		httpReq.Header.Set(name, value)
	}
	httpReq.Header.Del("Anthropic-Beta")
	if provider.APIKey != "" {
		switch upstreamAuthScheme(provider, outDialect) {
		case "x-api-key":
			httpReq.Header.Set("X-API-Key", provider.APIKey)
			httpReq.Header.Set("Anthropic-Version", "2023-06-01")
		case "x-goog-api-key":
			httpReq.Header.Set("X-Goog-Api-Key", provider.APIKey)
		case "replicate":
			httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
			httpReq.Header.Set("Prefer", "wait=60")
		default:
			httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
			if outDialect == "anthropic" {
				httpReq.Header.Set("Anthropic-Version", "2023-06-01")
			}
		}
	}
	httpResp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return
	}
	defer httpResp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxShadowResponseBytes))
	if err != nil {
		return
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return // failed=true
	}
	resp, err := decodeUpstreamResponse(outDialect, raw, target.Model)
	if err != nil {
		return
	}
	failed = false
	promptTokens = int64(resp.Usage.InputTokens)
	completionTokens = int64(resp.Usage.OutputTokens)
	costUSD = (float64(promptTokens)*target.InputPricePerMillionUSD + float64(completionTokens)*target.OutputPricePerMillionUSD) / 1e6
}

// ---- HTTP handlers ----

type configAdminShadowStartResponse struct {
	Shadow ShadowRun `json:"shadow"`
}

func (h *configAdminHandler) handleShadowStart(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorizeShadow(w, r, subject) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	var body ShadowStartRequest
	if !decodeConfigAdminBody(w, r, &body) {
		return
	}
	run, err := StartGroupShadow(h.deps.DB, h.deps.RuntimeScope, r.PathValue("group"), body, subject.subject, h.policy(), h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.ReloadShadows != nil {
		h.deps.ReloadShadows()
	}
	writeJSON(w, http.StatusCreated, configAdminShadowStartResponse{Shadow: *run})
}

func (h *configAdminHandler) authorizeShadow(w http.ResponseWriter, r *http.Request, subject adminAuthSubject) bool {
	if h.deps.Authorize == nil || !h.deps.Authorize(subject, adminPermissionShadowWrite) {
		if h.deps.RecordAccess != nil {
			h.deps.RecordAccess(r, subject, http.StatusForbidden, "shadow-forbidden", authzObjectAdminShadow, "write")
		}
		writeConfigAdminError(w, http.StatusForbidden, "shadow-forbidden", "the admin credential lacks the shadow:write permission")
		return false
	}
	return true
}

func (h *configAdminHandler) handleShadowStatus(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigRead) {
		return
	}
	var usageDB *gorm.DB
	if h.deps.UsageDB != nil {
		usageDB = h.deps.UsageDB()
	}
	comparison, err := GetGroupShadow(h.deps.DB, usageDB, h.deps.RuntimeScope, r.PathValue("group"), h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comparison)
}

func (h *configAdminHandler) handleShadowPromote(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	// Promotion is a shadow action and a group write; require both.
	if !h.authorizeShadow(w, r, subject) {
		return
	}
	if !h.authorize(w, r, subject, adminPermissionConfigWrite) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	var body struct {
		Reason string `json:"reason,omitempty"`
		Force  bool   `json:"force,omitempty"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		if !decodeConfigAdminBody(w, r, &body) {
			return
		}
	}
	if body.Force && !h.deps.Authorize(subject, adminPermissionConfigForce) {
		writeConfigAdminError(w, http.StatusForbidden, "config-force-required", "force requires the config:force permission")
		return
	}
	result, runID, err := PromoteGroupShadow(h.deps.DB, h.deps.RuntimeScope, r.PathValue("group"), subject.subject, body.Reason, body.Force, h.policy(), h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if !result.Replayed && h.deps.Reload != nil {
		_ = h.deps.Reload()
	}
	if h.deps.ReloadShadows != nil {
		h.deps.ReloadShadows()
	}
	resp := map[string]any{
		"group": result.Group, "set_id": result.SetID, "previous_set_id": result.PreviousSetID,
		"revision": result.Revision, "replayed": result.Replayed, "diff": result.Diff,
		"shadow_run_id": runID,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *configAdminHandler) handleShadowStop(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !h.authorizeShadow(w, r, subject) {
		return
	}
	if !h.writeGate(w) {
		return
	}
	var body configAdminRollbackRequest
	if r.Body != nil && r.ContentLength > 0 {
		if !decodeConfigAdminBody(w, r, &body) {
			return
		}
	}
	run, err := StopGroupShadow(h.deps.DB, h.deps.RuntimeScope, r.PathValue("group"), subject.subject, body.Reason, h.now())
	if err != nil {
		h.mapWriteError(w, err)
		return
	}
	if h.deps.ReloadShadows != nil {
		h.deps.ReloadShadows()
	}
	writeJSON(w, http.StatusOK, configAdminShadowStartResponse{Shadow: *run})
}
